//go:build integration

// Package concurrency implements the mandatory challenge scenarios (§13):
// the two-bets dispute, mass parallel replays, multi-instance processing and
// recovery after interruption, cross-checked against the ledger.
package concurrency_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

const defaultTestDSN = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"

func dsn(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultTestDSN
}

// instance is one independent application instance: its own pool and memory.
type instance struct {
	pool    *pgxpool.Pool
	wallets domain.WalletRepo
	txs     domain.TransactionRepo
	ledger  domain.LedgerRepo
	outbox  domain.OutboxRepo
	recon   domain.Reconstructor
	txm     *postgres.TxManager
	process *usecase.ProcessWager
	opener  *usecase.OpenWallet
}

func newInstance(t *testing.T) *instance {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, dsn(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)
	outbox := postgres.NewOutboxRepo(pool)
	txm := postgres.NewTxManager(pool)
	return &instance{
		pool: pool, wallets: wallets, txs: txs, ledger: ledger, outbox: outbox,
		recon: postgres.NewReconstructor(pool), txm: txm,
		process: usecase.NewProcessWager(txm, wallets, txs, ledger, outbox, nil),
		opener:  usecase.NewOpenWallet(txm, wallets, txs, ledger, outbox),
	}
}

// freshDatabase applies migrations and truncates everything once per test.
func freshDatabase(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := migrate.Run(dsn(t), migrations.FS, "up"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, wager_transactions, wallets, inbox, outbox CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func money(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, domain.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func betFor(w *domain.Wallet, extID, amount string) usecase.ProcessWagerInput {
	m, _ := domain.ParseMoney(amount, domain.BRL)
	return usecase.ProcessWagerInput{
		ProviderID:            "provider-a",
		ExternalTransactionID: extID,
		IdempotencyKey:        "provider-a:" + extID,
		PlayerID:              w.PlayerID(),
		WalletID:              w.ID(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  domain.KindBet,
		Money:                 m,
	}
}

// TestDisputedBetsTwoTimesEightyOverHundred is the mandatory scenario: one
// processed bet, one INSUFFICIENT_FUNDS rejection, balance 20.00 and exactly
// one debit — across three independent instances. Replays change nothing.
func TestDisputedBetsTwoTimesEightyOverHundred(t *testing.T) {
	freshDatabase(t)
	i1, i2, i3 := newInstance(t), newInstance(t), newInstance(t)
	ctx := context.Background()

	out, err := i1.opener.Execute(ctx, usecase.OpenWalletInput{PlayerID: "player-dispute", InitialBalance: money(t, "100.00")})
	if err != nil {
		t.Fatal(err)
	}
	w, err := i1.wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}

	// three instances dispute the same wallet concurrently
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		processed int
		rejected  int
	)
	bets := []struct {
		inst *instance
		ext  string
	}{{i1, "bet-80-a"}, {i2, "bet-80-b"}, {i3, "bet-80-replay-a"}}
	for _, b := range bets {
		wg.Add(1)
		go func(inst *instance, ext string) {
			defer wg.Done()
			out, err := inst.process.Execute(ctx, betFor(w, ext, "80.00"))
			mu.Lock()
			defer mu.Unlock()
			switch out.Status {
			case domain.StatusProcessed:
				processed++
			case domain.StatusRejected:
				rejected++
			}
			_ = err
		}(b.inst, b.ext)
	}
	wg.Wait()

	if processed != 1 || rejected != 2 {
		t.Fatalf("processed=%d rejected=%d, want 1 processed and 2 replays/rejections", processed, rejected)
	}

	// re-send both bets: nothing changes
	for _, inst := range []*instance{i1, i2} {
		for _, ext := range []string{"bet-80-a", "bet-80-b"} {
			out, err := inst.process.Execute(ctx, betFor(w, ext, "80.00"))
			if err != nil {
				t.Fatalf("replay %s: %v", ext, err)
			}
			if !out.Replay {
				t.Errorf("replay of %s must be idempotent", ext)
			}
		}
	}

	final, err := i1.wallets.GetByID(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	if final.Balance().Units() != 2000 {
		t.Errorf("balance = %s, want 20.00", final.Balance().String())
	}
	if final.Version() != 2 {
		t.Errorf("version = %d, want 2 (opening + one debit)", final.Version())
	}
	assertLedgerConsistent(t, i1, w.ID(), 1)
}

func assertLedgerConsistent(t *testing.T, inst *instance, walletID string, wantDebits int) {
	t.Helper()
	var debits int
	if err := inst.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM ledger_entries WHERE wallet_id = $1::uuid AND direction = 'DEBIT'`, walletID).Scan(&debits); err != nil {
		t.Fatal(err)
	}
	if debits != wantDebits {
		t.Errorf("debit entries = %d, want %d", debits, wantDebits)
	}
	stored, err := inst.wallets.GetByID(context.Background(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	calculated, count, err := inst.recon.RebuildBalance(context.Background(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Balance().Units() != calculated.Units() {
		t.Errorf("balance divergence: stored=%s calculated=%s (%d entries)",
			stored.Balance().String(), calculated.String(), count)
	}
}

// TestFiftyParallelReplays sends the same bet 50 times in parallel across
// three instances and proves a single debit with 49 replays.
func TestFiftyParallelReplays(t *testing.T) {
	freshDatabase(t)
	instances := []*instance{newInstance(t), newInstance(t), newInstance(t)}
	ctx := context.Background()

	out, err := instances[0].opener.Execute(ctx, usecase.OpenWalletInput{PlayerID: "player-50", InitialBalance: money(t, "100.00")})
	if err != nil {
		t.Fatal(err)
	}
	w, err := instances[0].wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}

	var (
		wg        sync.WaitGroup
		replays   atomic.Int64
		processed atomic.Int64
	)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		inst := instances[i%len(instances)]
		go func(n int, inst *instance) {
			defer wg.Done()
			res, err := inst.process.Execute(ctx, betFor(w, "bet-50", "10.00"))
			if err != nil {
				t.Errorf("send %d: %v", n, err)
				return
			}
			if res.Replay {
				replays.Add(1)
			} else {
				processed.Add(1)
			}
			// every response must carry the same observed balance
			if res.Balance.Units() != 9000 {
				t.Errorf("send %d reported balance %s, want 90.00", n, res.Balance.String())
			}
		}(i, inst)
	}
	wg.Wait()

	if processed.Load() != 1 || replays.Load() != 49 {
		t.Errorf("processed=%d replays=%d, want 1 and 49", processed.Load(), replays.Load())
	}
	assertLedgerConsistent(t, instances[0], w.ID(), 1)
}

// TestDistinctWalletsProcessInParallel proves there is no global lock: eight
// wallets on three instances with concurrent operations all land consistent.
func TestDistinctWalletsProcessInParallel(t *testing.T) {
	freshDatabase(t)
	instances := []*instance{newInstance(t), newInstance(t), newInstance(t)}
	ctx := context.Background()

	const n = 8
	walletIDs := make([]string, n)
	for i := 0; i < n; i++ {
		out, err := instances[0].opener.Execute(ctx, usecase.OpenWalletInput{
			PlayerID:       fmt.Sprintf("player-par-%d", i),
			InitialBalance: money(t, "100.00"),
		})
		if err != nil {
			t.Fatal(err)
		}
		walletIDs[i] = out.WalletID
	}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		inst := instances[i%len(instances)]
		go func(i int, inst *instance) {
			defer wg.Done()
			w, err := inst.wallets.GetByID(ctx, walletIDs[i])
			if err != nil {
				t.Error(err)
				return
			}
			for j := 0; j < 5; j++ {
				if _, err := inst.process.Execute(ctx, betFor(w, fmt.Sprintf("bet-%d-%d", i, j), "1.00")); err != nil {
					t.Errorf("wallet %d op %d: %v", i, j, err)
					return
				}
			}
		}(i, inst)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i := 0; i < n; i++ {
		w, err := instances[0].wallets.GetByID(ctx, walletIDs[i])
		if err != nil {
			t.Fatal(err)
		}
		if w.Balance().Units() != 9500 { // 100 - 5 bets of 1
			t.Errorf("wallet %d balance = %s, want 95.00", i, w.Balance().String())
		}
		assertLedgerConsistent(t, instances[0], walletIDs[i], 5)
	}
	t.Logf("8 wallets x 5 concurrent operations across 3 instances in %s (no global lock)", elapsed)
}

// TestRestartPreservesIdempotency simulates a full application restart: new
// pools over the same database; every replay and pending operation survives.
func TestRestartPreservesIdempotency(t *testing.T) {
	freshDatabase(t)
	first := newInstance(t)
	ctx := context.Background()

	out, err := first.opener.Execute(ctx, usecase.OpenWalletInput{PlayerID: "player-restart", InitialBalance: money(t, "100.00")})
	if err != nil {
		t.Fatal(err)
	}
	w, err := first.wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.process.Execute(ctx, betFor(w, "bet-before-restart", "30.00")); err != nil {
		t.Fatal(err)
	}

	// restart: brand-new instance over the same database
	restarted := newInstance(t)
	w2, err := restarted.wallets.GetByID(ctx, w.ID())
	if err != nil {
		t.Fatal(err)
	}
	res, err := restarted.process.Execute(ctx, betFor(w2, "bet-before-restart", "30.00"))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if !res.Replay || res.Balance.Units() != 7000 {
		t.Errorf("after restart: %+v, want replay with 70.00", res)
	}
	assertLedgerConsistent(t, restarted, w.ID(), 1)
}

// TestRefundBeforeBetResolvedByOtherInstance delivers the refund first and a
// reference worker on ANOTHER instance resolves it once the bet arrives.
func TestRefundBeforeBetResolvedByOtherInstance(t *testing.T) {
	freshDatabase(t)
	i1, i2 := newInstance(t), newInstance(t)
	ctx := context.Background()

	out, err := i1.opener.Execute(ctx, usecase.OpenWalletInput{PlayerID: "player-refcross", InitialBalance: money(t, "100.00")})
	if err != nil {
		t.Fatal(err)
	}
	w, err := i1.wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}

	refund := usecase.ProcessWagerInput{
		ProviderID: "provider-a", ExternalTransactionID: "refund-cross",
		IdempotencyKey: "provider-a:refund-cross", PlayerID: w.PlayerID(), WalletID: w.ID(),
		RoundID: "round-1", GameID: "fortune-chimp", Kind: domain.KindRefund,
		Money: money(t, "25.00"), ReferenceExternalTransactionID: "bet-cross",
	}
	if _, err := i1.process.Execute(ctx, refund); err != nil {
		t.Fatal(err)
	}

	// worker on instance 2 scans while the bet has not arrived
	workerDone := make(chan struct{})
	workerCtx, workerCancel := context.WithCancel(ctx)
	go func() {
		worker := usecase.NewProcessWager(i2.txm, i2.wallets, i2.txs, i2.ledger, i2.outbox, nil)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				close(workerDone)
				return
			case <-ticker.C:
				pending, err := i2.txs.ListPendingReference(ctx, 10)
				if err != nil {
					t.Error(err)
					continue
				}
				for _, tx := range pending {
					_ = i2.txm.WithinTx(ctx, func(ctx context.Context) error {
						return worker.Resume(ctx, tx)
					})
				}
			}
		}
	}()

	// the bet arrives on instance 1 later
	if _, err := i1.process.Execute(ctx, betFor(w, "bet-cross", "25.00")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tx, err := i2.txs.GetByProviderExternal(ctx, "provider-a", "refund-cross")
		if err == nil && tx.Status() == domain.StatusProcessed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	workerCancel()
	<-workerDone

	tx, err := i2.txs.GetByProviderExternal(ctx, "provider-a", "refund-cross")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Status() != domain.StatusProcessed {
		t.Fatalf("refund status = %s, want PROCESSED", tx.Status())
	}
	assertLedgerConsistent(t, i2, w.ID(), 1)
}
