package domain_test

import (
	"errors"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func TestLedgerEntryCreditConsistency(t *testing.T) {
	before := mustMoney(t, 100000, domain.BRL)
	after := mustMoney(t, 97500, domain.BRL)
	amount := mustMoney(t, 2500, domain.BRL)

	e, err := domain.NewLedgerEntry("wallet-1", "tx-1", domain.DebitDirection, amount, before, after)
	if err != nil {
		t.Fatalf("NewLedgerEntry: %v", err)
	}
	if e.WalletID() != "wallet-1" || e.TransactionID() != "tx-1" {
		t.Error("identity lost")
	}
	if e.Direction() != domain.DebitDirection {
		t.Errorf("direction = %s", e.Direction())
	}
	if e.BalanceBefore().Units() != 100000 || e.BalanceAfter().Units() != 97500 {
		t.Error("balances lost")
	}
}

func TestLedgerEntryDebitConsistency(t *testing.T) {
	before := mustMoney(t, 100000, domain.BRL)
	after := mustMoney(t, 100100, domain.BRL)
	amount := mustMoney(t, 100, domain.BRL)

	e, err := domain.NewLedgerEntry("w", "tx", domain.CreditDirection, amount, before, after)
	if err != nil {
		t.Fatalf("NewLedgerEntry: %v", err)
	}
	if e.Direction() != domain.CreditDirection {
		t.Errorf("direction = %s", e.Direction())
	}
}

func TestLedgerEntryInconsistentAmountRejected(t *testing.T) {
	before := mustMoney(t, 100000, domain.BRL)
	wrongAfter := mustMoney(t, 97000, domain.BRL)
	amount := mustMoney(t, 2500, domain.BRL)

	if _, err := domain.NewLedgerEntry("w", "tx", domain.DebitDirection, amount, before, wrongAfter); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("inconsistent debit entry: %v", err)
	}
}

func TestLedgerEntryWrongDirectionRejected(t *testing.T) {
	before := mustMoney(t, 100000, domain.BRL)
	after := mustMoney(t, 97500, domain.BRL)
	amount := mustMoney(t, 2500, domain.BRL)

	if _, err := domain.NewLedgerEntry("w", "tx", domain.CreditDirection, amount, before, after); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("credit with decreasing balance: %v", err)
	}
}

func TestLedgerEntryCurrencyMismatchRejected(t *testing.T) {
	before := mustMoney(t, 100000, domain.BRL)
	after := mustMoney(t, 100100, domain.BRL)
	amount := mustMoney(t, 100, "USD")

	if _, err := domain.NewLedgerEntry("w", "tx", domain.CreditDirection, amount, before, after); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("mixed currencies: %v", err)
	}
}

func TestLedgerEntryZeroAmountRejected(t *testing.T) {
	zero, _ := domain.Zero(domain.BRL)
	before := mustMoney(t, 100000, domain.BRL)
	if _, err := domain.NewLedgerEntry("w", "tx", domain.DebitDirection, zero, before, before); err == nil {
		t.Error("zero amount must be rejected")
	}
}

func TestLedgerEntryEmptyIdentityRejected(t *testing.T) {
	before := mustMoney(t, 100, domain.BRL)
	after := mustMoney(t, 101, domain.BRL)
	amount := mustMoney(t, 1, domain.BRL)

	if _, err := domain.NewLedgerEntry("", "tx", domain.CreditDirection, amount, before, after); err == nil {
		t.Error("empty wallet id must be rejected")
	}
	if _, err := domain.NewLedgerEntry("w", "", domain.CreditDirection, amount, before, after); err == nil {
		t.Error("empty transaction id must be rejected")
	}
}
