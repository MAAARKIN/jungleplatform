package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Direction string

const (
	CreditDirection Direction = "CREDIT"
	DebitDirection  Direction = "DEBIT"
)

// LedgerEntry is an immutable double of one balance movement. Its construction
// validates the arithmetic invariant balanceAfter = balanceBefore ± money
// according to the direction. LOSS and rejected operations never produce
// entries. Persistence must forbid UPDATE and DELETE.
type LedgerEntry struct {
	id            string
	walletID      string
	transactionID string
	direction     Direction
	money         Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

// NewLedgerEntry builds and validates one append-only entry, assigning a new id.
func NewLedgerEntry(walletID, transactionID string, dir Direction, money, balanceBefore, balanceAfter Money) (LedgerEntry, error) {
	return buildLedgerEntry(uuid.NewString(), walletID, transactionID, dir, money, balanceBefore, balanceAfter, time.Now().UTC())
}

// RehydrateLedgerEntry rebuilds a persisted entry without revalidating the
// movement; it still verifies the arithmetic invariant so a corrupt row never
// becomes a live value.
func RehydrateLedgerEntry(id, walletID, transactionID string, dir Direction, money, balanceBefore, balanceAfter Money, createdAt time.Time) (LedgerEntry, error) {
	return buildLedgerEntry(id, walletID, transactionID, dir, money, balanceBefore, balanceAfter, createdAt)
}

func buildLedgerEntry(id, walletID, transactionID string, dir Direction, money, balanceBefore, balanceAfter Money, createdAt time.Time) (LedgerEntry, error) {
	if id == "" {
		return LedgerEntry{}, fmt.Errorf("%w: entry id is required", ErrInvalidInput)
	}
	if walletID == "" || transactionID == "" {
		return LedgerEntry{}, fmt.Errorf("%w: wallet and transaction are required", ErrInvalidInput)
	}
	if money.currency == "" || money.currency != balanceBefore.currency || balanceBefore.currency != balanceAfter.currency {
		return LedgerEntry{}, ErrCurrencyMismatch
	}
	if !money.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: ledger amount must be positive", ErrInvalidInput)
	}
	var consistent bool
	switch dir {
	case CreditDirection:
		consistent = balanceAfter.units == balanceBefore.units+money.units
	case DebitDirection:
		consistent = balanceAfter.units == balanceBefore.units-money.units
	default:
		return LedgerEntry{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidInput, dir)
	}
	if !consistent {
		return LedgerEntry{}, fmt.Errorf("%w: balanceAfter must equal balanceBefore %s money", ErrInvalidInput, dir)
	}
	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     dir,
		money:         money,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

// ID returns the entry identifier.
func (e LedgerEntry) ID() string { return e.id }

// WalletID returns the affected wallet.
func (e LedgerEntry) WalletID() string { return e.walletID }

// TransactionID returns the originating transaction.
func (e LedgerEntry) TransactionID() string { return e.transactionID }

// Direction returns CREDIT or DEBIT.
func (e LedgerEntry) Direction() Direction { return e.direction }

// Money returns the moved amount.
func (e LedgerEntry) Money() Money { return e.money }

// BalanceBefore returns the wallet balance before the movement.
func (e LedgerEntry) BalanceBefore() Money { return e.balanceBefore }

// BalanceAfter returns the wallet balance after the movement.
func (e LedgerEntry) BalanceAfter() Money { return e.balanceAfter }

// CreatedAt returns the entry instant (UTC).
func (e LedgerEntry) CreatedAt() time.Time { return e.createdAt }
