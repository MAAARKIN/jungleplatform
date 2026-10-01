//go:build integration

package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/outbox"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
)

const (
	defaultTestDSN     = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"
	defaultSQSEndpoint = "http://localhost:4566"
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
	pool   *pgxpool.Pool
	client *sqs.Client
	outbox domain.OutboxRepo
	events string
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

	events := fmt.Sprintf("%s/000000000000/wager-events.fifo", sqsEndpoint(t))
	drain(t, client, events)
	return &harness{pool: pool, client: client, outbox: postgres.NewOutboxRepo(pool), events: events}
}

// drain removes leftovers of previous runs from the events queue.
func drain(t *testing.T, client *sqs.Client, queue string) {
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

func enqueue(t *testing.T, h *harness, n int) []domain.Event {
	t.Helper()
	ctx := context.Background()
	var events []domain.Event
	for i := 0; i < n; i++ {
		e := domain.NewWalletBalanceChanged(
			uuid.NewString(), "corr", fmt.Sprintf("wallet-%d", i), "tx-1",
			domain.CreditDirection,
			mustMoney(t, "10.00"), mustMoney(t, "0.00"), mustMoney(t, "10.00"),
			1, time.Now().UTC(),
		)
		if err := h.outbox.Enqueue(ctx, e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func mustMoney(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, domain.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func runUntil(t *testing.T, pub *outbox.Publisher, cond func() bool, what string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("publisher run: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("publisher did not stop after %s", what)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	t.Fatalf("timed out waiting for %s", what)
}

func receiveAll(t *testing.T, client *sqs.Client, queue string) []map[string]any {
	t.Helper()
	ctx := context.Background()
	var out []map[string]any
	for {
		msgs, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queue),
			MaxNumberOfMessages: 10,
			VisibilityTimeout:   1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs.Messages) == 0 {
			return out
		}
		for _, m := range msgs.Messages {
			var env map[string]any
			if err := json.Unmarshal([]byte(aws.ToString(m.Body)), &env); err != nil {
				t.Errorf("invalid event envelope: %v", err)
				continue
			}
			out = append(out, env)
			_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(queue),
				ReceiptHandle: m.ReceiptHandle,
			})
		}
	}
}

func TestPublisherPublishesCommittedEvents(t *testing.T) {
	h := newHarness(t)
	events := enqueue(t, h, 3)
	want := map[string]bool{}
	for _, e := range events {
		want[e.EventID] = true
	}

	pub := outbox.NewPublisher(h.pool, h.outbox, h.client, h.events, "publisher-1", slog.Default(), 200*time.Millisecond)
	runUntil(t, pub, func() bool {
		var pending int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending)
		return pending == 0
	}, "all events published")

	received := receiveAll(t, h.client, h.events)
	got := map[string]bool{}
	for _, env := range received {
		id, _ := env["eventId"].(string)
		got[id] = true
		if env["eventType"] != "WalletBalanceChanged" {
			t.Errorf("eventType = %v", env["eventType"])
		}
	}
	if len(got) != len(want) {
		t.Errorf("received %d unique events, want %d", len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Errorf("event %s not published", id)
		}
	}
}

func TestPublisherTwoInstancesDispute(t *testing.T) {
	h := newHarness(t)
	events := enqueue(t, h, 20)

	p1 := outbox.NewPublisher(h.pool, h.outbox, h.client, h.events, "publisher-1", slog.Default(), 100*time.Millisecond)
	p2 := outbox.NewPublisher(h.pool, h.outbox, h.client, h.events, "publisher-2", slog.Default(), 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- p1.Run(ctx) }()
	go func() { done <- p2.Run(ctx) }()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var pending int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending)
		if pending == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	<-done

	received := receiveAll(t, h.client, h.events)
	seen := map[string]int{}
	for _, env := range received {
		id, _ := env["eventId"].(string)
		seen[id]++
	}
	if len(seen) != len(events) {
		t.Errorf("published %d unique events, want %d", len(seen), len(events))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("event %s published %d times, want exactly 1", id, n)
		}
	}
}

func TestPublisherReschedulesOnFailure(t *testing.T) {
	h := newHarness(t)
	events := enqueue(t, h, 1)

	// bad queue URL forces a publish failure
	pub := outbox.NewPublisher(h.pool, h.outbox, h.client, "http://localhost:4566/000000000000/no-such-queue.fifo", "publisher-1", slog.Default(), 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pub.Run(ctx) }()
	time.Sleep(700 * time.Millisecond)
	cancel()
	<-done

	var attempts int
	var next *time.Time
	if err := h.pool.QueryRow(context.Background(),
		`SELECT attempts, next_attempt_at FROM outbox WHERE event_id = $1::uuid`, events[0].EventID).Scan(&attempts, &next); err != nil {
		t.Fatal(err)
	}
	if attempts < 1 {
		t.Errorf("attempts = %d, want >= 1", attempts)
	}
	if next == nil || !next.After(time.Now().UTC().Add(time.Second)) {
		t.Errorf("next_attempt_at = %v, want in the future (backoff)", next)
	}
	var published *time.Time
	if err := h.pool.QueryRow(context.Background(),
		`SELECT published_at FROM outbox WHERE event_id = $1::uuid`, events[0].EventID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != nil {
		t.Error("failed publish must not be marked as published")
	}
}

func TestPublisherReclaimsAbandonedClaim(t *testing.T) {
	h := newHarness(t)
	events := enqueue(t, h, 1)

	// simulate a dead publisher: claim then abandon (lease expires)
	if _, err := h.outbox.ClaimBatch(context.Background(), "dead-publisher", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE outbox SET next_attempt_at = now() - interval '1 hour' WHERE event_id = $1::uuid`, events[0].EventID); err != nil {
		t.Fatal(err)
	}

	pub := outbox.NewPublisher(h.pool, h.outbox, h.client, h.events, "publisher-2", slog.Default(), 100*time.Millisecond)
	runUntil(t, pub, func() bool {
		var pending int
		_ = h.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending)
		return pending == 0
	}, "abandoned event reclaimed and published")

	received := receiveAll(t, h.client, h.events)
	if len(received) != 1 {
		t.Fatalf("received %d events, want 1", len(received))
	}
	if id, _ := received[0]["eventId"].(string); id != events[0].EventID {
		t.Errorf("eventId = %s, want %s (identity preserved across publishers)", id, events[0].EventID)
	}
}
