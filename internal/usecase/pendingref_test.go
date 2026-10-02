//go:build integration

package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/pendingref"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// refundInput builds a REFUND for the given reference external id.
func refundInput(w *domain.Wallet, extID, refExtID, amount string) usecase.ProcessWagerInput {
	m, _ := domain.ParseMoney(amount, domain.BRL)
	return usecase.ProcessWagerInput{
		ProviderID:                     "provider-a",
		ExternalTransactionID:          extID,
		IdempotencyKey:                 "provider-a:" + extID,
		PlayerID:                       w.PlayerID(),
		WalletID:                       w.ID(),
		RoundID:                        "round-1",
		GameID:                         "fortune-chimp",
		Kind:                           domain.KindRefund,
		Money:                          m,
		ReferenceExternalTransactionID: refExtID,
	}
}

func waitReferenceResolved(t *testing.T, s *stack, worker *pendingref.Worker, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			cancel()
			<-done
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("timed out waiting for reference resolution")
}

func TestPendingRefundResolvedAfterBetArrives(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-resolve")
	worker := pendingref.NewWorker(s.pool, s.tx, s.txs, s.p, time.Hour, 50, nil, 100*time.Millisecond)

	// refund arrives BEFORE the bet
	if _, err := s.p.Execute(ctx, refundInput(w, "ref-1", "bet-ref-1", "25.00")); err != nil {
		t.Fatalf("refund: %v", err)
	}

	var pending int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	// bet arrives later
	if _, err := s.p.Execute(ctx, betInput(w, "25.00", "bet-ref-1")); err != nil {
		t.Fatal(err)
	}

	waitReferenceResolved(t, s, worker, func() bool {
		var processed int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'ref-1' AND status = 'PROCESSED'`).Scan(&processed)
		return processed == 1
	})

	var balance int64
	if err := s.pool.QueryRow(ctx, `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	// 100.00 - 25.00 (bet) + 25.00 (refund) = 100.00
	if balance != 10000 {
		t.Errorf("balance = %d, want 100.00", balance)
	}
	var credits int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM ledger_entries WHERE direction = 'CREDIT' AND money_units = 2500`).Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if credits != 1 {
		t.Errorf("refund credit entries = %d, want 1", credits)
	}
}

func TestPendingReferenceExpiryRejects(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-expiry")
	worker := pendingref.NewWorker(s.pool, s.tx, s.txs, s.p, 50*time.Millisecond, 50, nil, 50*time.Millisecond)

	if _, err := s.p.Execute(ctx, refundInput(w, "ref-exp", "never-arrives", "25.00")); err != nil {
		t.Fatal(err)
	}
	// backdate the creation beyond the TTL
	if _, err := s.pool.Exec(ctx, `UPDATE wager_transactions SET created_at = now() - interval '1 hour' WHERE external_transaction_id = 'ref-exp'`); err != nil {
		t.Fatal(err)
	}

	waitReferenceResolved(t, s, worker, func() bool {
		var rejected int
		_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'ref-exp' AND status = 'REJECTED' AND failure_code = 'REFERENCE_NOT_FOUND'`).Scan(&rejected)
		return rejected == 1
	})

	var events int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE event_type = 'WagerTransactionRejected'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("rejected events = %d, want 1", events)
	}
	var balance int64
	if err := s.pool.QueryRow(ctx, `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 10000 {
		t.Errorf("expired refund must not move the balance: %d", balance)
	}
}

func TestReferenceExistsButRejectedFailsFast(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-rejected-ref")

	// the referenced bet is rejected (insufficient funds)
	bad := betInput(w, "500.00", "bet-rejected-ref")
	if _, err := s.p.Execute(ctx, bad); err != nil {
		t.Fatal(err)
	}

	// a refund referencing it arrives → immediate rejection
	if _, err := s.p.Execute(ctx, refundInput(w, "ref-fast", "bet-rejected-ref", "500.00")); err != nil {
		t.Fatal(err)
	}

	var status string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE external_transaction_id = 'ref-fast'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.StatusRejected) {
		t.Errorf("status = %s, want REJECTED (fail fast on unsuccessful reference)", status)
	}
}

func TestReferenceStillPendingKeepsWaiting(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-wait")

	// refund references a bet that has NOT arrived yet
	if _, err := s.p.Execute(ctx, refundInput(w, "ref-wait", "bet-wait", "25.00")); err != nil {
		t.Fatal(err)
	}

	worker := pendingref.NewWorker(s.pool, s.tx, s.txs, s.p, time.Hour, 50, nil, 50*time.Millisecond)
	ctx2, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx2) }()
	time.Sleep(600 * time.Millisecond) // a few retry ticks with no bet
	cancel()
	<-done

	var status string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM wager_transactions WHERE external_transaction_id = 'ref-wait'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.StatusPendingReference) {
		t.Errorf("status = %s, want still PENDING_REFERENCE", status)
	}
	var attempts int
	if err := s.pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE external_transaction_id = 'ref-wait'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts < 1 {
		t.Errorf("reference_attempts = %d, want > 0 (durable backoff)", attempts)
	}
}

func TestDuplicateReversalBlocked(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-dup")

	// bet processed
	if _, err := s.p.Execute(ctx, betInput(w, "25.00", "bet-dup")); err != nil {
		t.Fatal(err)
	}
	// first refund succeeds
	if _, err := s.p.Execute(ctx, refundInput(w, "ref-dup-1", "bet-dup", "25.00")); err != nil {
		t.Fatal(err)
	}
	// second refund on the same bet → ALREADY_REVERSED
	_, err := s.p.Execute(ctx, refundInput(w, "ref-dup-2", "bet-dup", "25.00"))
	if err != nil {
		t.Fatalf("second refund should return a rejection output, not an error: %v", err)
	}
	var failure string
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(failure_code,'') FROM wager_transactions WHERE external_transaction_id = 'ref-dup-2'`).Scan(&failure); err != nil {
		t.Fatal(err)
	}
	if failure != "ALREADY_REVERSED" {
		t.Errorf("failure = %q, want ALREADY_REVERSED", failure)
	}
	var balance int64
	if err := s.pool.QueryRow(ctx, `SELECT balance_units FROM wallets WHERE id = $1::uuid`, w.ID()).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	// 100 - 25 (bet) + 25 (refund) = 100: no double return
	if balance != 10000 {
		t.Errorf("balance = %d, want 100.00 (single return)", balance)
	}
}

func TestRollbackOfWinDebitsAndExceedingBalanceRejected(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	w := wallet100(t, s, "t-ref-rollback")

	winIn := betInput(w, "60.00", "win-rb")
	winIn.Kind = domain.KindWin
	if _, err := s.p.Execute(ctx, winIn); err != nil {
		t.Fatal(err)
	}

	rbIn := betInput(w, "60.00", "rb-win")
	rbIn.Kind = domain.KindRollback
	rbIn.ReferenceExternalTransactionID = "win-rb"
	out, err := s.p.Execute(ctx, rbIn)
	if err != nil {
		t.Fatalf("rollback of win: %v", err)
	}
	if out.Status != domain.StatusProcessed || out.Balance.Units() != 10000 {
		t.Errorf("out = %+v, want processed with 100.00", out)
	}

	// win 200 (balance 300), bet 250 (balance 50): rolling the win back would
	// debit 200 from a 50.00 balance → REVERSAL_EXCEEDS_BALANCE
	win2 := betInput(w, "200.00", "win-rb2")
	win2.Kind = domain.KindWin
	if _, err := s.p.Execute(ctx, win2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Execute(ctx, betInput(w, "250.00", "bet-spender")); err != nil {
		t.Fatal(err)
	}
	rb2 := betInput(w, "200.00", "rb-win2")
	rb2.Kind = domain.KindRollback
	rb2.ReferenceExternalTransactionID = "win-rb2"
	out2, err := s.p.Execute(ctx, rb2)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if out2.Status != domain.StatusRejected || out2.FailureCode != usecase.FailureReversalExceedsBalance {
		t.Errorf("out = %+v, want REJECTED/REVERSAL_EXCEEDS_BALANCE", out2)
	}
	var failure string
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(failure_code,'') FROM wager_transactions WHERE external_transaction_id = 'rb-win2'`).Scan(&failure); err != nil {
		t.Fatal(err)
	}
	if failure != usecase.FailureReversalExceedsBalance {
		t.Errorf("failure = %q, want REVERSAL_EXCEEDS_BALANCE", failure)
	}
}
