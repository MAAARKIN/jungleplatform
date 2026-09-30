package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Movement is the ledger data produced by a balance change.
type Movement struct {
	BalanceBefore Money
	BalanceAfter  Money
}

// Credit is the Movement returned by a successful Wallet.Credit.
type Credit Movement

// Debit is the Movement returned by a successful Wallet.Debit.
type Debit Movement

// Wallet is the financial aggregate root. The balance changes only through
// Credit/Debit; every movement must be persisted as a ledger entry in the
// same SQL transaction. The pair (playerID, currency) identifies one wallet.
type Wallet struct {
	id        string
	playerID  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// NewWallet opens a wallet for a player with a non-negative initial balance.
// The initial version is 1.
func NewWallet(playerID string, balance Money, now time.Time) (*Wallet, error) {
	if playerID == "" || balance.currency == "" {
		return nil, ErrInvalidInput
	}
	if !balance.IsZero() && !balance.IsPositive() {
		return nil, fmt.Errorf("%w: initial balance cannot be negative", ErrInvalidInput)
	}
	return &Wallet{
		id:        uuid.NewString(),
		playerID:  playerID,
		balance:   balance,
		version:   1,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

// RehydrateWallet rebuilds a persisted wallet without reapplying movements.
// It validates the stored state: empty identity, negative balance and
// non-positive version are rejected.
func RehydrateWallet(id, playerID string, balance Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	if id == "" || playerID == "" || balance.currency == "" || version < 1 {
		return nil, ErrInvalidInput
	}
	if !balance.IsZero() && !balance.IsPositive() {
		return nil, fmt.Errorf("%w: stored balance cannot be negative", ErrInvalidInput)
	}
	return &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt.UTC(),
		updatedAt: updatedAt.UTC(),
	}, nil
}

// ID returns the wallet identifier.
func (w *Wallet) ID() string { return w.id }

// PlayerID returns the owning player.
func (w *Wallet) PlayerID() string { return w.playerID }

// Balance returns the current balance.
func (w *Wallet) Balance() Money { return w.balance }

// Currency returns the wallet currency.
func (w *Wallet) Currency() Currency { return w.balance.currency }

// Version returns the aggregate version: 1 at creation, incremented on every
// balance change.
func (w *Wallet) Version() int64 { return w.version }

// CreatedAt returns the creation instant.
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }

// UpdatedAt returns the last balance-change instant.
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

// Credit adds money to the wallet. Zero amounts are rejected: a credit must
// represent a real movement.
func (w *Wallet) Credit(m Money) (Credit, error) {
	if m.currency != w.balance.currency {
		return Credit{}, ErrCurrencyMismatch
	}
	if !m.IsPositive() {
		return Credit{}, fmt.Errorf("%w: credit amount must be positive", ErrInvalidInput)
	}
	after, err := w.balance.Add(m)
	if err != nil {
		return Credit{}, err
	}
	before := w.balance
	w.balance = after
	w.version++
	w.updatedAt = time.Now().UTC()
	return Credit{BalanceBefore: before, BalanceAfter: after}, nil
}

// Debit removes money from the wallet. The balance must remain >= zero and
// zero amounts are rejected.
func (w *Wallet) Debit(m Money) (Debit, error) {
	if m.currency != w.balance.currency {
		return Debit{}, ErrCurrencyMismatch
	}
	if !m.IsPositive() {
		return Debit{}, fmt.Errorf("%w: debit amount must be positive", ErrInvalidInput)
	}
	if m.units > w.balance.units {
		return Debit{}, ErrInsufficientFunds
	}
	after, err := w.balance.Sub(m)
	if err != nil {
		return Debit{}, err
	}
	before := w.balance
	w.balance = after
	w.version++
	w.updatedAt = time.Now().UTC()
	return Debit{BalanceBefore: before, BalanceAfter: after}, nil
}
