//go:build integration

package usecase_test

import (
	"context"
	"errors"
	"os"
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

type stack struct {
	pool          *pgxpool.Pool
	wallets       domain.WalletRepo
	txs           domain.TransactionRepo
	ledger        domain.LedgerRepo
	outbox        domain.OutboxRepo
	inbox         domain.InboxRepo
	reconstructor domain.Reconstructor
	p             *usecase.ProcessWager
	open          *usecase.OpenWallet
}

func newStack(t *testing.T) *stack {
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
	wallets := postgres.NewWalletRepo(pool)
	txs := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)
	inbox := postgres.NewInboxRepo(pool)
	outbox := postgres.NewOutboxRepo(pool)
	txm := postgres.NewTxManager(pool)
	return &stack{
		pool:          pool,
		wallets:       wallets,
		txs:           txs,
		ledger:        ledger,
		outbox:        outbox,
		inbox:         inbox,
		reconstructor: postgres.NewReconstructor(pool),
		p:             usecase.NewProcessWager(txm, wallets, txs, ledger, outbox),
		open:          usecase.NewOpenWallet(txm, wallets, txs, ledger, outbox),
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

func wallet100(t *testing.T, s *stack, playerID string) *domain.Wallet {
	t.Helper()
	w, err := domain.NewWallet(playerID, money(t, "100.00"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.wallets.Insert(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	return w
}

func betInput(w *domain.Wallet, amount string, extID string) usecase.ProcessWagerInput {
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

func TestBetProcessesAtomically(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-bet")

	out, err := s.p.Execute(context.Background(), betInput(w, "25.00", "bet-1"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Status != domain.StatusProcessed || out.Replay {
		t.Errorf("out = %+v", out)
	}
	if out.Balance.Units() != 7500 {
		t.Errorf("balance = %s, want 75.00", out.Balance.String())
	}

	var entries, events int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("ledger entries = %d, want 1", entries)
	}
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("outbox events = %d, want 2", events)
	}
}

func TestBetReplayReturnsOriginalBalance(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-replay")

	first, err := s.p.Execute(context.Background(), betInput(w, "25.00", "bet-1"))
	if err != nil {
		t.Fatal(err)
	}
	// another movement lands on the wallet after the original processing
	if _, err := w.Credit(money(t, "10.00")); err != nil {
		t.Fatal(err)
	}
	if err := s.wallets.Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	second, err := s.p.Execute(context.Background(), betInput(w, "25.00", "bet-1"))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replay {
		t.Error("second call must be a replay")
	}
	if second.Balance.Units() != first.Balance.Units() {
		t.Errorf("replay balance = %s, want original %s", second.Balance.String(), first.Balance.String())
	}

	var entries int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("replay created extra ledger entries: %d", entries)
	}
}

func TestSameKeyDifferentPayloadConflicts(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-conflict")

	if _, err := s.p.Execute(context.Background(), betInput(w, "25.00", "bet-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Execute(context.Background(), betInput(w, "30.00", "bet-1")); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestSameExternalIDWithAnotherKeyConflicts(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-extconflict")

	in := betInput(w, "25.00", "bet-1")
	if _, err := s.p.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "provider-a:other-key"
	if _, err := s.p.Execute(context.Background(), in); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestBetWithoutFundsIsRejectedAndReplayable(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-insufficient")

	out, err := s.p.Execute(context.Background(), betInput(w, "100.01", "bet-a"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Status != domain.StatusRejected || out.FailureCode != usecase.FailureInsufficientFunds {
		t.Fatalf("out = %+v", out)
	}

	replay, err := s.p.Execute(context.Background(), betInput(w, "100.01", "bet-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay || replay.Status != domain.StatusRejected || replay.FailureCode != usecase.FailureInsufficientFunds {
		t.Errorf("replay = %+v", replay)
	}

	var entries int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Errorf("rejected bet must not move the ledger: %d entries", entries)
	}
}

func TestLossCompletesWithoutLedgerOrBalanceEvent(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-loss")

	in := betInput(w, "0.00", "loss-1")
	in.Kind = domain.KindLoss
	out, err := s.p.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Status != domain.StatusProcessed || out.Balance.Units() != 10000 {
		t.Errorf("out = %+v", out)
	}

	var entries, processed, balanceChanged int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE event_type = 'WagerTransactionProcessed'`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE event_type = 'WalletBalanceChanged'`).Scan(&balanceChanged); err != nil {
		t.Fatal(err)
	}
	if entries != 0 || processed != 1 || balanceChanged != 0 {
		t.Errorf("loss must produce only the processed event: %d %d %d", entries, processed, balanceChanged)
	}
}

func TestWinCreditsWallet(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-win")

	in := betInput(w, "10.00", "win-1")
	in.Kind = domain.KindWin
	out, err := s.p.Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.StatusProcessed || out.Balance.Units() != 11000 {
		t.Errorf("out = %+v", out)
	}
	var direction string
	if err := s.pool.QueryRow(context.Background(), `SELECT direction FROM ledger_entries`).Scan(&direction); err != nil {
		t.Fatal(err)
	}
	if direction != "CREDIT" {
		t.Errorf("direction = %s", direction)
	}
}

func TestWalletNotFoundPropagates(t *testing.T) {
	s := newStack(t)
	w := wallet100(t, s, "t-pw-missing")
	in := betInput(w, "25.00", "bet-missing")
	in.WalletID = "00000000-0000-0000-0000-000000000000"
	if _, err := s.p.Execute(context.Background(), in); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
