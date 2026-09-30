package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

var fixedNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func newWallet100(t *testing.T) *domain.Wallet {
	t.Helper()
	w, err := domain.NewWallet("player-1", mustMoney(t, 100000, domain.BRL), fixedNow)
	if err != nil {
		t.Fatalf("NewWallet: %v", err)
	}
	return w
}

func mustMoney(t *testing.T, units int64, c domain.Currency) domain.Money {
	t.Helper()
	m, err := domain.NewMoney(units, c)
	if err != nil {
		t.Fatalf("NewMoney(%d, %s): %v", units, c, err)
	}
	return m
}

func TestNewWalletCreatesAtVersionOne(t *testing.T) {
	balance := mustMoney(t, 100000, domain.BRL)
	w, err := domain.NewWallet("player-1", balance, fixedNow)
	if err != nil {
		t.Fatalf("NewWallet: %v", err)
	}
	if w.Version() != 1 {
		t.Errorf("version = %d, want 1", w.Version())
	}
	if w.Balance().Units() != 100000 {
		t.Errorf("balance = %d, want 100000", w.Balance().Units())
	}
	if w.PlayerID() != "player-1" {
		t.Errorf("playerID = %q", w.PlayerID())
	}
	if w.ID() == "" {
		t.Error("wallet id must be generated")
	}
	if w.Currency() != domain.BRL {
		t.Errorf("currency = %s", w.Currency())
	}
	if w.CreatedAt() != fixedNow || w.UpdatedAt() != fixedNow {
		t.Error("timestamps must come from the passed clock")
	}
}

func TestNewWalletRejectsNegativeBalance(t *testing.T) {
	if _, err := domain.NewWallet("p", mustMoney(t, -1, domain.BRL), fixedNow); err == nil {
		t.Error("expected error for negative opening balance")
	}
}

func TestNewWalletRejectsEmptyPlayerOrCurrency(t *testing.T) {
	if _, err := domain.NewWallet("", mustMoney(t, 0, domain.BRL), fixedNow); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("empty player: %v", err)
	}
	if _, err := domain.NewWallet("p", domain.Money{}, fixedNow); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("empty currency: %v", err)
	}
}

func TestDebitUpdatesBalanceAndVersion(t *testing.T) {
	w := newWallet100(t)
	d, err := w.Debit(mustMoney(t, 2500, domain.BRL))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if d.BalanceBefore.Units() != 100000 || d.BalanceAfter.Units() != 97500 {
		t.Errorf("debit deltas = %d -> %d", d.BalanceBefore.Units(), d.BalanceAfter.Units())
	}
	if w.Balance().Units() != 97500 {
		t.Errorf("balance = %d, want 97500", w.Balance().Units())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
}

func TestCreditUpdatesBalanceAndVersion(t *testing.T) {
	w := newWallet100(t)
	c, err := w.Credit(mustMoney(t, 100, domain.BRL))
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	if c.BalanceBefore.Units() != 100000 || c.BalanceAfter.Units() != 100100 {
		t.Errorf("credit deltas = %d -> %d", c.BalanceBefore.Units(), c.BalanceAfter.Units())
	}
	if w.Version() != 2 {
		t.Errorf("version = %d, want 2", w.Version())
	}
}

func TestDebitBelowZeroRejected(t *testing.T) {
	w := newWallet100(t)
	if _, err := w.Debit(mustMoney(t, 100001, domain.BRL)); !errors.Is(err, domain.ErrInsufficientFunds) {
		t.Errorf("err = %v, want ErrInsufficientFunds", err)
	}
	if w.Balance().Units() != 100000 || w.Version() != 1 {
		t.Error("rejected debit must not mutate the wallet")
	}
}

func TestDebitZeroRejected(t *testing.T) {
	w := newWallet100(t)
	if _, err := w.Debit(mustMoney(t, 0, domain.BRL)); err == nil {
		t.Error("zero debit must be rejected")
	}
}

func TestCreditZeroRejected(t *testing.T) {
	w := newWallet100(t)
	if _, err := w.Credit(mustMoney(t, 0, domain.BRL)); err == nil {
		t.Error("zero credit must be rejected")
	}
}

func TestMovementCurrencyMustMatchWallet(t *testing.T) {
	w := newWallet100(t)
	if _, err := w.Debit(mustMoney(t, 100, "USD")); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestRehydratePreservesIdentityAndVersion(t *testing.T) {
	created := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Hour)
	w, err := domain.RehydrateWallet("w-1", "player-1", mustMoney(t, 5000, domain.BRL), 7, created, updated)
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}
	if w.ID() != "w-1" || w.PlayerID() != "player-1" || w.Version() != 7 {
		t.Error("rehydration lost identity fields")
	}
	if w.CreatedAt() != created || w.UpdatedAt() != updated {
		t.Error("rehydration lost timestamps")
	}
	d, err := w.Debit(mustMoney(t, 100, domain.BRL))
	if err != nil {
		t.Fatalf("Debit after rehydrate: %v", err)
	}
	if d.BalanceBefore.Units() != 5000 || w.Version() != 8 {
		t.Error("movement after rehydration behaves like a fresh wallet")
	}
}

func TestRehydrateRejectsInvalidState(t *testing.T) {
	if _, err := domain.RehydrateWallet("", "p", mustMoney(t, 1, domain.BRL), 1, fixedNow, fixedNow); err == nil {
		t.Error("empty id must be rejected")
	}
	if _, err := domain.RehydrateWallet("w", "", mustMoney(t, 1, domain.BRL), 1, fixedNow, fixedNow); err == nil {
		t.Error("empty player must be rejected")
	}
	if _, err := domain.RehydrateWallet("w", "p", mustMoney(t, 1, domain.BRL), 0, fixedNow, fixedNow); err == nil {
		t.Error("version 0 must be rejected")
	}
	if _, err := domain.RehydrateWallet("w", "p", mustMoney(t, -1, domain.BRL), 1, fixedNow, fixedNow); err == nil {
		t.Error("negative balance must be rejected")
	}
}
