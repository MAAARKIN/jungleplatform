//go:build integration

package postgres_test

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
)

const defaultDSN = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"

func dsn(t *testing.T) string {
	t.Helper()
	// Integration tests never touch the development database: they run on
	// jungle_test unless TEST_POSTGRES_DSN points elsewhere.
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultDSN
}

func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	if err := migrate.Run(dsn(t), migrations.FS, "up"); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := postgres.NewPool(ctx, dsn(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// clean slate: TRUNCATE bypasses the append-only row triggers (they only
	// guard UPDATE/DELETE), giving every test a deterministic empty database
	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, wager_transactions, wallets, inbox, outbox CASCADE`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	return pool
}

func wallet(t *testing.T, playerID string, units int64) *domain.Wallet {
	t.Helper()
	w, err := domain.NewWallet(playerID, domain.NewMoneyUnchecked(units, domain.BRL), time.Now().UTC())
	if err != nil {
		t.Fatalf("NewWallet: %v", err)
	}
	return w
}

func TestWalletInsertGetRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	w := wallet(t, "t-wallet-roundtrip", 100000)
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByID(ctx, w.ID())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID() != w.ID() || got.PlayerID() != w.PlayerID() {
		t.Error("identity lost")
	}
	if got.Balance().Units() != 100000 || got.Balance().Currency() != domain.BRL {
		t.Errorf("balance lost: %+v", got.Balance())
	}
	if got.Version() != 1 || got.Currency() != domain.BRL {
		t.Error("version/currency lost")
	}
}

func TestWalletInsertDuplicatePlayerCurrencyConflict(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	first := wallet(t, "t-wallet-dup", 100)
	if err := repo.Insert(ctx, first); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	second := wallet(t, "t-wallet-dup", 200)
	if err := repo.Insert(ctx, second); !errors.Is(err, domain.ErrWalletConflict) {
		t.Errorf("err = %v, want ErrWalletConflict", err)
	}
}

func TestWalletGetNotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	if _, err := repo.GetByID(context.Background(), "00000000-0000-0000-0000-000000000000"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestWithinTxRollsBackOnError(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	w := wallet(t, "t-wallet-rollback", 100)
	err := postgres.WithinTx(ctx, pool, func(ctx context.Context) error {
		if err := repo.Insert(ctx, w); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected boom error")
	}
	if _, err := repo.GetByID(ctx, w.ID()); !errors.Is(err, domain.ErrNotFound) {
		t.Error("rolled-back wallet must not be visible")
	}
}

func TestWithinTxCommitsChanges(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	w := wallet(t, "t-wallet-commit", 100)
	err := postgres.WithinTx(ctx, pool, func(ctx context.Context) error {
		return repo.Insert(ctx, w)
	})
	if err != nil {
		t.Fatalf("WithinTx: %v", err)
	}
	if _, err := repo.GetByID(ctx, w.ID()); err != nil {
		t.Errorf("committed wallet must be visible: %v", err)
	}
}

func TestGetForUpdateSerializesWriters(t *testing.T) {
	pool := newTestPool(t)
	second, err := postgres.NewPool(context.Background(), dsn(t))
	if err != nil {
		t.Fatalf("second pool: %v", err)
	}
	defer second.Close()

	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	w := wallet(t, "t-wallet-lock", 100000)
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	locked := false
	err = postgres.WithinTx(ctx, pool, func(ctx context.Context) error {
		if _, err := repo.GetForUpdate(ctx, w.ID()); err != nil {
			return err
		}
		locked = true

		lockCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := second.Exec(lockCtx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, w.ID())
		if err == nil {
			t.Error("second writer must block while row is locked")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithinTx: %v", err)
	}
	if !locked {
		t.Fatal("lock not taken")
	}

	lockCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := second.Exec(lockCtx, `SELECT id FROM wallets WHERE id = $1 FOR UPDATE`, w.ID()); err != nil {
		t.Errorf("after commit the row must be lockable: %v", err)
	}
}

func TestUpdatePersistsNewBalanceAndVersion(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewWalletRepo(pool)
	ctx := context.Background()

	w := wallet(t, "t-wallet-update", 100000)
	if err := repo.Insert(ctx, w); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := w.Debit(domain.NewMoneyUnchecked(2500, domain.BRL)); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if err := repo.Update(ctx, w); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, w.ID())
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Balance().Units() != 97500 || got.Version() != 2 {
		t.Errorf("persisted = %d v%d, want 97500 v2", got.Balance().Units(), got.Version())
	}
}
