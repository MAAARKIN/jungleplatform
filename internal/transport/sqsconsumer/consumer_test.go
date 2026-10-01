//go:build integration

package sqsconsumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
	"github.com/maaarkin/jungleplatform/internal/transport/sqsconsumer"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

const (
	defaultTestDSN     = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"
	defaultSQSEndpoint = "http://localhost:4566"
	consumerName       = "sqs-consumer-test"
)

func dsn(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultTestDSN
}

func sqsEndpoint(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_SQS_ENDPOINT"); v != "" {
		return v
	}
	return defaultSQSEndpoint
}

type harness struct {
	pool    *pgxpool.Pool
	client  *sqs.Client
	queue   string
	dlq     string
	inbox   domain.InboxRepo
	wallets domain.WalletRepo
	txs     domain.TransactionRepo
	ledger  domain.LedgerRepo
	outbox  domain.OutboxRepo
	process *usecase.ProcessWager
	opener  *usecase.OpenWallet
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	if err := migrate.Run(dsn(t), migrations.FS, "up"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, wager_transactions, wallets, inbox, outbox CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(sqsEndpoint(t)) })

	// the compose-provisioned test queue (visibility 2s, redrive to the DLQ
	// after 2 receives); drain leftovers from previous runs
	dlq := fmt.Sprintf("%s/000000000000/wager-transactions-dlq.fifo", strings.TrimSuffix(sqsEndpoint(t), ""))
	queue := fmt.Sprintf("%s/000000000000/wager-transactions-test.fifo", strings.TrimSuffix(sqsEndpoint(t), ""))
	drainQueue(t, client, queue)

	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)
	outbox := postgres.NewOutboxRepo(pool)
	txm := postgres.NewTxManager(pool)
	return &harness{
		pool:    pool,
		client:  client,
		queue:   queue,
		dlq:     dlq,
		inbox:   postgres.NewInboxRepo(pool),
		wallets: wallets,
		txs:     txs,
		ledger:  ledger,
		outbox:  outbox,
		process: usecase.NewProcessWager(txm, wallets, txs, ledger, outbox),
		opener:  usecase.NewOpenWallet(txm, wallets, txs, ledger, outbox),
	}
}

// drainQueue removes any leftover messages from previous runs.
func drainQueue(t *testing.T, client *sqs.Client, queue string) {
	t.Helper()
	ctx := context.Background()
	for {
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queue),
			MaxNumberOfMessages: 10,
			VisibilityTimeout:   0,
		})
		if err != nil || len(out.Messages) == 0 {
			return
		}
		for _, m := range out.Messages {
			_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queue),
				ReceiptHandle: m.ReceiptHandle,
			})
		}
	}
}

func (h *harness) newConsumer(t *testing.T) *sqsconsumer.Consumer {
	t.Helper()
	return sqsconsumer.NewConsumer(h.client, h.queue, consumerName, h.process, h.inbox, postgres.NewTxManager(h.pool), slog.Default(), 1)
}

func (h *harness) openWallet(t *testing.T, playerID, amount string) *domain.Wallet {
	t.Helper()
	out, err := h.opener.Execute(context.Background(), usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: mustMoney(t, amount),
	})
	if err != nil {
		t.Fatalf("open wallet: %v", err)
	}
	w, err := h.wallets.GetByID(context.Background(), out.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func mustMoney(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, domain.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func envelope(t *testing.T, messageID string, w *domain.Wallet, extID, kind, amount, idempotencyKey string, reference string) []byte {
	t.Helper()
	data := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": extID,
		"idempotencyKey":        idempotencyKey,
		"playerId":              w.PlayerID(),
		"walletId":              w.ID(),
		"roundId":               "round-1",
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money":                 map[string]any{"amount": amount, "currency": "BRL"},
	}
	if reference != "" {
		data["referenceExternalTransactionId"] = reference
	}
	body, err := json.Marshal(map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data":       data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (h *harness) send(t *testing.T, queue string, body []byte) {
	h.sendWithDedup(t, queue, body, uuid.NewString())
}

func (h *harness) sendWithDedup(t *testing.T, queue string, body []byte, dedupID string) {
	t.Helper()
	ctx := context.Background()
	_, err := h.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(queue),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String("provider-a"),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (h *harness) runUntil(t *testing.T, consumer *sqsconsumer.Consumer, cond func() bool, what string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("consumer run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("consumer did not stop after %s", what)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) queueCount(t *testing.T, queue string) int32 {
	t.Helper()
	out, err := h.client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queue),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
	})
	if err != nil {
		t.Fatal(err)
	}
	visible := mustAtoi(t, out.Attributes["ApproximateNumberOfMessages"])
	notVisible := mustAtoi(t, out.Attributes["ApproximateNumberOfMessagesNotVisible"])
	return visible + notVisible
}

func mustAtoi(t *testing.T, s string) int32 {
	t.Helper()
	var n int32
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("atoi %q: %v", s, err)
	}
	return n
}

func TestConsumerProcessesAndDeletesMessage(t *testing.T) {
	h := newHarness(t)
	w := h.openWallet(t, "t-sqs-process", "100.00")
	c := h.newConsumer(t)

	h.send(t, h.queue, envelope(t, "msg-process-1", w, "ext-1", "BET", "10.00", "provider-a:ext-1", ""))

	h.runUntil(t, c, func() bool {
		var count int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'ext-1'`).Scan(&count)
		return count == 1
	}, "transaction processing")

	if got := h.queueCount(t, h.queue); got != 0 {
		t.Errorf("message must be deleted after commit, queue = %d", got)
	}
	var balance int64
	if err := h.pool.QueryRow(context.Background(), `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 9000 {
		t.Errorf("balance = %d, want 90.00", balance)
	}
	var completed int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox WHERE message_id = 'msg-process-1' AND completed_at IS NOT NULL`).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Errorf("inbox completion = %d", completed)
	}
}

func TestConsumerDeduplicatesRepeatDelivery(t *testing.T) {
	h := newHarness(t)
	w := h.openWallet(t, "t-sqs-dup", "100.00")
	c := h.newConsumer(t)

	body := envelope(t, "msg-dup-1", w, "ext-dup", "BET", "10.00", "provider-a:ext-dup", "")
	// three deliveries of the SAME message identity, distinct SQS dedup ids:
	// exercises the application-level inbox, not the broker deduplication
	h.sendWithDedup(t, h.queue, body, "d-1")
	h.sendWithDedup(t, h.queue, body, "d-2")
	h.sendWithDedup(t, h.queue, body, "d-3")

	h.runUntil(t, c, func() bool {
		var inboxRows int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox WHERE message_id = 'msg-dup-1'`).Scan(&inboxRows)
		var debits int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE direction = 'DEBIT'`).Scan(&debits)
		return inboxRows >= 1 && h.queueCount(t, h.queue) == 0 && debits == 1
	}, "deduplicated processing")

	var debits int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE direction = 'DEBIT'`).Scan(&debits); err != nil {
		t.Fatal(err)
	}
	if debits != 1 {
		t.Errorf("debit entries = %d, want exactly 1", debits)
	}
	var balance int64
	if err := h.pool.QueryRow(context.Background(), `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 9000 {
		t.Errorf("balance = %d, want a single debit (90.00)", balance)
	}
}

func TestConsumerRejectionIsTerminalAndRemovesMessage(t *testing.T) {
	h := newHarness(t)
	w := h.openWallet(t, "t-sqs-reject", "100.00")
	c := h.newConsumer(t)

	h.send(t, h.queue, envelope(t, "msg-reject", w, "ext-reject", "BET", "200.00", "provider-a:ext-reject", ""))

	h.runUntil(t, c, func() bool {
		var count int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM wager_transactions WHERE failure_code = 'INSUFFICIENT_FUNDS'`).Scan(&count)
		return count == 1 && h.queueCount(t, h.queue) == 0
	}, "rejection and message removal")

	if got := h.queueCount(t, h.queue); got != 0 {
		t.Errorf("confirmed rejection must remove the message, queue = %d", got)
	}
}

func TestConsumerInvalidPayloadRedrivesToDLQ(t *testing.T) {
	h := newHarness(t)
	c := h.newConsumer(t)

	h.send(t, h.queue, []byte(`{not json`))

	attrs, err := h.client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(h.queue),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameRedrivePolicy,
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("queue attrs: %v", attrs.Attributes)

	// one long-lived consumer: redrive accumulates receiveCount across
	// deliveries (visibility timeout 2s, maxReceiveCount 2)
	h.runUntil(t, c, func() bool {
		return h.queueCount(t, h.dlq) > 0
	}, "invalid message reaching the DLQ")
}

func TestConsumerCrossChannelIdempotency(t *testing.T) {
	h := newHarness(t)
	w := h.openWallet(t, "t-sqs-cross", "100.00")
	c := h.newConsumer(t)

	// processed via HTTP-equivalent path first (same use case, same key)
	if _, err := h.process.Execute(context.Background(), usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-cross",
		IdempotencyKey:        "provider-a:ext-cross",
		PlayerID:              w.PlayerID(),
		WalletID:              w.ID(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  domain.KindBet,
		Money:                 mustMoney(t, "10.00"),
	}); err != nil {
		t.Fatal(err)
	}

	// same operation arrives via SQS with the same key → replay, no double debit
	h.send(t, h.queue, envelope(t, "msg-cross", w, "ext-cross", "BET", "10.00", "provider-a:ext-cross", ""))

	h.runUntil(t, c, func() bool {
		var inboxRows int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox WHERE message_id = 'msg-cross' AND completed_at IS NOT NULL`).Scan(&inboxRows)
		return inboxRows == 1
	}, "cross-channel replay")

	var debits int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE direction = 'DEBIT'`).Scan(&debits); err != nil {
		t.Fatal(err)
	}
	if debits != 1 {
		t.Errorf("cross-channel debit count = %d, want exactly 1", debits)
	}
	var balance int64
	if err := h.pool.QueryRow(context.Background(), `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 9000 {
		t.Errorf("balance = %d, want 90.00", balance)
	}
}

func TestConsumerStopsOnCancel(t *testing.T) {
	h := newHarness(t)
	c := h.newConsumer(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop on cancel")
	}
}
