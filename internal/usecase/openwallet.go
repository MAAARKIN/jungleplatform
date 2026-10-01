// Package usecase holds the application use cases shared by the HTTP API and
// the SQS consumer, guaranteeing identical financial behavior on both paths.
package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// OpenWallet opens a new player wallet in a single SQL commit. A positive
// initial balance also creates the internal OPENING transaction (processed),
// its ledger credit and the WagerTransactionProcessed + WalletBalanceChanged
// outbox events in that same commit. A zero balance creates none of those.
type OpenWallet struct {
	Tx           domain.TxManager
	Wallets      domain.WalletRepo
	Transactions domain.TransactionRepo
	Ledger       domain.LedgerRepo
	Outbox       domain.OutboxRepo
}

// OpenWalletInput carries the request data.
type OpenWalletInput struct {
	PlayerID       string
	InitialBalance domain.Money
}

// OpenWalletOutput returns the created wallet state.
type OpenWalletOutput struct {
	WalletID string
	PlayerID string
	Balance  domain.Money
	Version  int64
}

// NewOpenWallet builds the use case with its repositories.
func NewOpenWallet(
	tx domain.TxManager,
	wallets domain.WalletRepo,
	transactions domain.TransactionRepo,
	ledger domain.LedgerRepo,
	outbox domain.OutboxRepo,
) *OpenWallet {
	return &OpenWallet{
		Tx: tx, Wallets: wallets, Transactions: transactions, Ledger: ledger, Outbox: outbox,
	}
}

// Execute opens the wallet atomically.
func (u *OpenWallet) Execute(ctx context.Context, in OpenWalletInput) (OpenWalletOutput, error) {
	var out OpenWalletOutput
	err := u.Tx.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := u.Wallets.GetByPlayerAndCurrency(ctx, in.PlayerID, in.InitialBalance.Currency())
		if err == nil {
			_ = existing
			return domain.ErrWalletConflict
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		now := time.Now().UTC()
		w, err := domain.NewWallet(in.PlayerID, in.InitialBalance, now)
		if err != nil {
			return err
		}
		if err := u.Wallets.Insert(ctx, w); err != nil {
			return err
		}

		if in.InitialBalance.IsPositive() {
			if err := u.recordOpening(ctx, w, in.InitialBalance, now); err != nil {
				return err
			}
		}

		out = OpenWalletOutput{WalletID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version()}
		return nil
	})
	if err != nil {
		return OpenWalletOutput{}, err
	}
	return out, nil
}

func (u *OpenWallet) recordOpening(ctx context.Context, w *domain.Wallet, balance domain.Money, now time.Time) error {
	tx, err := domain.NewOpeningTransaction(w.PlayerID(), w.ID(), balance.Currency(), balance.Units(), now)
	if err != nil {
		return err
	}
	if err := tx.MarkProcessed(balance); err != nil {
		return err
	}
	if err := u.Transactions.Insert(ctx, tx); err != nil {
		return err
	}

	zero := domain.NewMoneyUnchecked(0, balance.Currency())
	entry, err := domain.NewLedgerEntry(w.ID(), tx.ID(), domain.CreditDirection, balance, zero, balance)
	if err != nil {
		return err
	}
	if err := u.Ledger.Insert(ctx, entry); err != nil {
		return err
	}

	correlationID := uuid.NewString()
	if err := u.Outbox.Enqueue(ctx, domain.NewWagerTransactionProcessed(
		uuid.NewString(), correlationID, tx.ID(), "", "", domain.KindOpening, balance, balance, now,
	)); err != nil {
		return err
	}
	return u.Outbox.Enqueue(ctx, domain.NewWalletBalanceChanged(
		uuid.NewString(), correlationID, w.ID(), tx.ID(),
		domain.CreditDirection, balance, zero, balance, w.Version(), now,
	))
}
