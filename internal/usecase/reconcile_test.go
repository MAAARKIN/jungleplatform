//go:build integration

package usecase_test

import (
	"context"
	"errors"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// reconciled stack: open wallet through the use case so the opening ledger
// entry exists.
func TestReconcileConsistentWallet(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	out, err := s.open.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       "t-rc-player",
		InitialBalance: money(t, "100.00"),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	w, err := s.wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.p.Execute(ctx, betInput(w, "25.00", "rc-bet-1")); err != nil {
		t.Fatalf("bet: %v", err)
	}

	rec := usecase.NewReconcile(s.wallets, s.reconstructor)
	res, err := rec.Execute(ctx, usecase.ReconcileInput{WalletID: out.WalletID})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !res.Consistent {
		t.Errorf("expected consistent, got %+v", res)
	}
	if res.StoredBalance.Units() != 7500 || res.CalculatedBalance.Units() != 7500 {
		t.Errorf("balances = %s / %s, want 75.00 both", res.StoredBalance.String(), res.CalculatedBalance.String())
	}
	if !res.Difference.IsZero() {
		t.Errorf("difference = %s, want 0.00", res.Difference.String())
	}
	if res.CheckedEntries != 2 {
		t.Errorf("checkedEntries = %d, want 2 (opening + bet)", res.CheckedEntries)
	}
}

func TestReconcileReportsDivergenceWithoutFixing(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	out, err := s.open.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       "t-rc-div",
		InitialBalance: money(t, "100.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// simulate corruption: stored balance diverges from the ledger
	if _, err := s.pool.Exec(ctx, `UPDATE wallets SET balance_units = 9000 WHERE id = $1::uuid`, out.WalletID); err != nil {
		t.Fatal(err)
	}

	rec := usecase.NewReconcile(s.wallets, s.reconstructor)
	res, err := rec.Execute(ctx, usecase.ReconcileInput{WalletID: out.WalletID})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Consistent {
		t.Errorf("expected divergence: %+v", res)
	}
	if res.StoredBalance.Units() != 9000 || res.CalculatedBalance.Units() != 10000 {
		t.Errorf("balances = %s / %s, want 90.00 / 100.00", res.StoredBalance.String(), res.CalculatedBalance.String())
	}
	if res.Difference.Units() != -1000 {
		t.Errorf("difference = %s, want -10.00 (stored - calculated)", res.Difference.String())
	}

	// reconciliation must not fix the stored balance
	w, err := s.wallets.GetByID(ctx, out.WalletID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance().Units() != 9000 {
		t.Errorf("reconcile mutated the balance: %s", w.Balance().String())
	}
}

func TestReconcileZeroBalanceWalletWithoutLedger(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	out, err := s.open.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       "t-rc-zero",
		InitialBalance: money(t, "0.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := usecase.NewReconcile(s.wallets, s.reconstructor)
	res, err := rec.Execute(ctx, usecase.ReconcileInput{WalletID: out.WalletID})
	if err != nil {
		t.Fatalf("zero-balance wallet must reconcile: %v", err)
	}
	if !res.Consistent || !res.CalculatedBalance.IsZero() || res.CheckedEntries != 0 {
		t.Errorf("res = %+v", res)
	}
	if res.CalculatedBalance.Currency() != domain.BRL {
		t.Errorf("currency lost: %s", res.CalculatedBalance.Currency())
	}
}

func TestReconcileUnknownWalletNotFound(t *testing.T) {
	s := newStack(t)
	rec := usecase.NewReconcile(s.wallets, s.reconstructor)
	if _, err := rec.Execute(context.Background(), usecase.ReconcileInput{WalletID: "00000000-0000-0000-0000-000000000000"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
