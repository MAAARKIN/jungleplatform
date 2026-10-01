//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
)

func externalTxInput(playerID, walletID string, kind domain.Kind, units int64) domain.ExternalTransactionInput {
	return domain.ExternalTransactionInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-" + playerID,
		IdempotencyKey:        "provider-a:ext-" + playerID,
		PayloadHash:           "hash-" + playerID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 domain.NewMoneyUnchecked(units, domain.BRL),
	}
}

func seedWallet(t *testing.T, repo domain.WalletRepo, playerID string, units int64) *domain.Wallet {
	t.Helper()
	w := wallet(t, playerID, units)
	if err := repo.Insert(context.Background(), w); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return w
}

func TestTransactionInsertAndGetRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)

	w := seedWallet(t, wallets, "t-tx-roundtrip", 100000)
	tx, err := domain.NewExternalTransaction(externalTxInput(w.PlayerID(), w.ID(), domain.KindBet, 2500), fixedNow)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	if err := txs.Insert(ctx, tx); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	byID, err := txs.GetByID(ctx, tx.ID())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.ProviderID() != tx.ProviderID() || byID.ExternalTransactionID() != tx.ExternalTransactionID() ||
		byID.IdempotencyKey() != tx.IdempotencyKey() || byID.PayloadHash() != tx.PayloadHash() {
		t.Error("external identity lost")
	}
	if byID.RoundID() != tx.RoundID() || byID.GameID() != tx.GameID() || byID.Money().Units() != 2500 {
		t.Error("payload fields lost")
	}
	if byID.Status() != domain.StatusPending || byID.Source() != domain.SourceExternal {
		t.Error("state lost")
	}

	byKey, err := txs.GetByIdempotencyKey(ctx, tx.IdempotencyKey())
	if err != nil || byKey.ID() != tx.ID() {
		t.Errorf("GetByIdempotencyKey: %v", err)
	}
	byExt, err := txs.GetByProviderExternal(ctx, "provider-a", tx.ExternalTransactionID())
	if err != nil || byExt.ID() != tx.ID() {
		t.Errorf("GetByProviderExternal: %v", err)
	}
}

func TestTransactionUpdatePersistsTransition(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)

	w := seedWallet(t, wallets, "t-tx-update", 100000)
	tx, err := domain.NewExternalTransaction(externalTxInput(w.PlayerID(), w.ID(), domain.KindBet, 2500), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := txs.Insert(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkProcessed(domain.NewMoneyUnchecked(97500, domain.BRL)); err != nil {
		t.Fatal(err)
	}
	if err := txs.Update(ctx, tx); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := txs.GetByID(ctx, tx.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != domain.StatusProcessed || got.FailureCode() != "" {
		t.Errorf("status/failure = %s/%q", got.Status(), got.FailureCode())
	}
	if !got.HasResultBalance() || got.ResultBalance().Units() != 97500 {
		t.Error("result balance lost")
	}
}

func TestTransactionUpdatePersistsRejection(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)

	w := seedWallet(t, wallets, "t-tx-rejected", 100)
	tx, err := domain.NewExternalTransaction(externalTxInput(w.PlayerID(), w.ID(), domain.KindBet, 80000), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := txs.Insert(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkRejected("INSUFFICIENT_FUNDS"); err != nil {
		t.Fatal(err)
	}
	if err := txs.Update(ctx, tx); err != nil {
		t.Fatal(err)
	}

	got, err := txs.GetByID(ctx, tx.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != domain.StatusRejected || got.FailureCode() != "INSUFFICIENT_FUNDS" {
		t.Errorf("status/failure = %s/%q", got.Status(), got.FailureCode())
	}
	if got.HasResultBalance() {
		t.Error("rejected transaction must not carry result balance")
	}
}

func TestTransactionNotFound(t *testing.T) {
	pool := newTestPool(t)
	txs := postgres.NewTransactionRepo(pool)
	if _, err := txs.GetByID(context.Background(), "00000000-0000-0000-0000-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLedgerInsertAndCursorPagination(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)

	w := seedWallet(t, wallets, "t-ledger-page", 0)
	walletID := w.ID()

	// one real transaction per entry: (wallet_id, transaction_id) is unique
	var txIDs []string
	for i := 0; i < 3; i++ {
		tx, err := domain.NewOpeningTransaction(w.PlayerID(), walletID, domain.BRL, 1000, fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := txs.Insert(ctx, tx); err != nil {
			t.Fatal(err)
		}
		txIDs = append(txIDs, tx.ID())
		time.Sleep(2 * time.Millisecond) // distinct created_at ordering
		before := domain.NewMoneyUnchecked(int64(i)*1000, domain.BRL)
		after := domain.NewMoneyUnchecked(int64(i+1)*1000, domain.BRL)
		e, err := domain.NewLedgerEntry(walletID, txIDs[i], domain.CreditDirection,
			domain.NewMoneyUnchecked(1000, domain.BRL), before, after)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if err := ledger.Insert(ctx, e); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	page1, cursor, err := ledger.ListByWallet(ctx, walletID, "", 2)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 || cursor == "" {
		t.Fatalf("page1 = %d entries, cursor empty=%v", len(page1), cursor == "")
	}
	page2, cursor2, err := ledger.ListByWallet(ctx, walletID, cursor, 2)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 1 || cursor2 != "" {
		t.Errorf("page2 = %d entries, cursor2 empty=%v", len(page2), cursor2 == "")
	}
	if page1[0].TransactionID() == page2[0].TransactionID() {
		t.Error("cursor continuation repeated an entry")
	}
}

func TestLedgerInsertDuplicateTransactionRejected(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)

	w := seedWallet(t, wallets, "t-ledger-dup", 0)
	tx, err := domain.NewOpeningTransaction(w.PlayerID(), w.ID(), domain.BRL, 100, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := txs.Insert(ctx, tx); err != nil {
		t.Fatal(err)
	}

	e, err := domain.NewLedgerEntry(w.ID(), tx.ID(), domain.CreditDirection,
		domain.NewMoneyUnchecked(100, domain.BRL),
		domain.NewMoneyUnchecked(0, domain.BRL), domain.NewMoneyUnchecked(100, domain.BRL))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Insert(ctx, e); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	second, err := domain.NewLedgerEntry(w.ID(), tx.ID(), domain.CreditDirection,
		domain.NewMoneyUnchecked(100, domain.BRL),
		domain.NewMoneyUnchecked(100, domain.BRL), domain.NewMoneyUnchecked(200, domain.BRL))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Insert(ctx, second); err == nil || !errors.Is(err, domain.ErrDuplicateLedgerEntry) {
		t.Errorf("err = %v, want ErrDuplicateLedgerEntry", err)
	}
}

func TestInboxTryRegisterAndComplete(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	inbox := postgres.NewInboxRepo(pool)

	first, err := inbox.TryRegister(ctx, "worker-1", "msg-1", "hash-a")
	if err != nil || !first {
		t.Fatalf("first TryRegister = %v, %v", first, err)
	}
	dup, err := inbox.TryRegister(ctx, "worker-1", "msg-1", "hash-a")
	if err != nil || dup {
		t.Errorf("second TryRegister = %v, %v (want false)", dup, err)
	}
	if ok, err := inbox.TryRegister(ctx, "worker-1", "msg-1", "hash-b"); err == nil || ok {
		t.Error("hash mismatch on re-delivery must be rejected")
	}
	if err := inbox.Complete(ctx, "worker-1", "msg-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var completed *time.Time
	if err := pool.QueryRow(ctx, `SELECT completed_at FROM inbox WHERE consumer_name = 'worker-1' AND message_id = 'msg-1'`).Scan(&completed); err != nil || completed == nil {
		t.Errorf("completed_at not persisted: %v %v", completed, err)
	}
}

func TestOutboxEnqueueClaimPublish(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	outbox := postgres.NewOutboxRepo(pool)

	var ids []string
	for i := 0; i < 3; i++ {
		e := domain.NewWalletBalanceChanged(
			uuid.NewString(), "corr", "wallet-1", "tx-1",
			domain.DebitDirection,
			domain.NewMoneyUnchecked(2500, domain.BRL),
			domain.NewMoneyUnchecked(100000, domain.BRL),
			domain.NewMoneyUnchecked(97500, domain.BRL),
			2, fixedNow,
		)
		if err := outbox.Enqueue(ctx, e); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		ids = append(ids, e.EventID)
	}

	batch1, err := outbox.ClaimBatch(ctx, "publisher-1", 2)
	if err != nil {
		t.Fatalf("ClaimBatch p1: %v", err)
	}
	if len(batch1) != 2 {
		t.Fatalf("p1 claimed %d, want 2", len(batch1))
	}
	batch2, err := outbox.ClaimBatch(ctx, "publisher-2", 10)
	if err != nil {
		t.Fatalf("ClaimBatch p2: %v", err)
	}
	if len(batch2) != 1 {
		t.Fatalf("p2 claimed %d, want 1 (claimed rows must not be re-claimed)", len(batch2))
	}

	if err := outbox.MarkPublished(ctx, ids[0], time.Now().UTC()); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}

	if err := outbox.Reschedule(ctx, ids[1], time.Now().UTC().Add(time.Hour), 1); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at = now() - interval '1 hour' WHERE event_id = $1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := outbox.ClaimBatch(ctx, "publisher-3", 10)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(reclaimed) != 1 || reclaimed[0].EventID != ids[2] {
		t.Errorf("reclaimed %v, want %s", reclaimed, ids[2])
	}

	after, err := outbox.ClaimBatch(ctx, "publisher-4", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range after {
		if m.EventID == ids[0] {
			t.Error("published event must not be claimable")
		}
	}
}

func TestOutboxClaimPreservesEnvelope(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	outbox := postgres.NewOutboxRepo(pool)

	e := domain.NewWalletBalanceChanged(
		uuid.NewString(), "corr-42", "wallet-9", "tx-9",
		domain.CreditDirection,
		domain.NewMoneyUnchecked(100000, domain.BRL),
		domain.NewMoneyUnchecked(0, domain.BRL),
		domain.NewMoneyUnchecked(100000, domain.BRL),
		1, fixedNow,
	)
	if err := outbox.Enqueue(ctx, e); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	batch, err := outbox.ClaimBatch(ctx, "p", 10)
	if err != nil || len(batch) != 1 {
		t.Fatalf("ClaimBatch: %v %d", err, len(batch))
	}
	got := batch[0]
	if got.EventID != e.EventID {
		t.Errorf("eventId = %s, want %s", got.EventID, e.EventID)
	}
	var env map[string]any
	if err := json.Unmarshal(got.Payload, &env); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if env["eventType"] != domain.EventTypeWalletBalanceChanged || env["aggregateId"] != "wallet-9" || env["correlationId"] != "corr-42" {
		t.Errorf("envelope lost: %v", env)
	}
	if env["occurredAt"] != "2026-09-30T12:00:00Z" {
		t.Errorf("occurredAt = %v", env["occurredAt"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok || data["walletId"] != "wallet-9" || data["walletVersion"] != float64(1) {
		t.Errorf("payload lost: %v", env["data"])
	}
	ba, ok := data["balanceAfter"].(map[string]any)
	if !ok || ba["amount"] != "1000.00" || ba["currency"] != "BRL" {
		t.Errorf("balanceAfter lost: %v", data["balanceAfter"])
	}
}

func TestRebuildBalanceMatchesLedger(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)
	rec := postgres.NewReconstructor(pool)

	w := seedWallet(t, wallets, "t-rebuild", 0)
	walletID := w.ID()

	opening, err := domain.NewOpeningTransaction(w.PlayerID(), walletID, domain.BRL, 100000, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := txs.Insert(ctx, opening); err != nil {
		t.Fatal(err)
	}
	bet, err := domain.NewExternalTransaction(externalTxInput(w.PlayerID(), walletID, domain.KindBet, 2500), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := txs.Insert(ctx, bet); err != nil {
		t.Fatal(err)
	}

	if err := ledger.Insert(ctx, mustEntry(t, walletID, opening.ID(), domain.CreditDirection, 100000, 0, 100000)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Insert(ctx, mustEntry(t, walletID, bet.ID(), domain.DebitDirection, 2500, 100000, 97500)); err != nil {
		t.Fatal(err)
	}

	balance, count, err := rec.RebuildBalance(ctx, walletID)
	if err != nil {
		t.Fatalf("RebuildBalance: %v", err)
	}
	if balance.Units() != 97500 || balance.Currency() != domain.BRL {
		t.Errorf("balance = %s", balance.String())
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func mustEntry(t *testing.T, walletID, txID string, dir domain.Direction, amount, before, after int64) domain.LedgerEntry {
	t.Helper()
	e, err := domain.NewLedgerEntry(walletID, txID, dir,
		domain.NewMoneyUnchecked(amount, domain.BRL),
		domain.NewMoneyUnchecked(before, domain.BRL),
		domain.NewMoneyUnchecked(after, domain.BRL))
	if err != nil {
		t.Fatalf("entry %s: %v", txID, err)
	}
	return e
}
