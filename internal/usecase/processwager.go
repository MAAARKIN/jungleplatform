package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// Stable rejection/failure codes.
const (
	FailureInsufficientFunds      = "INSUFFICIENT_FUNDS"
	FailureReversalExceedsBalance = "REVERSAL_EXCEEDS_BALANCE"
	FailureCurrencyMismatch       = "CURRENCY_MISMATCH"
	FailureReferenceNotFound      = "REFERENCE_NOT_FOUND"
	FailureReferencePending       = "REFERENCE_PENDING"
	FailureReferenceUnsuccessful  = "REFERENCE_UNSUCCESSFUL"
	FailureReferenceMismatch      = "REFERENCE_MISMATCH"
)

// ProcessWager processes one externally originated wagering operation with
// persistent idempotency. It is shared by the HTTP API and the SQS consumer,
// guaranteeing identical financial behavior and replay semantics.
type ProcessWager struct {
	Tx           domain.TxManager
	Wallets      domain.WalletRepo
	Transactions domain.TransactionRepo
	Ledger       domain.LedgerRepo
	Outbox       domain.OutboxRepo
}

// NewProcessWager builds the use case with its repositories.
func NewProcessWager(
	tx domain.TxManager,
	wallets domain.WalletRepo,
	transactions domain.TransactionRepo,
	ledger domain.LedgerRepo,
	outbox domain.OutboxRepo,
) *ProcessWager {
	return &ProcessWager{Tx: tx, Wallets: wallets, Transactions: transactions, Ledger: ledger, Outbox: outbox}
}

// ProcessWagerInput carries the validated business payload.
type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           domain.Kind
	Money                          domain.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

// ProcessWagerOutput returns the processing result. On a replay, Balance is
// the wallet balance observed at the original processing.
type ProcessWagerOutput struct {
	TransactionID string
	Status        domain.Status
	Balance       domain.Money
	Replay        bool
	FailureCode   string
}

// Execute runs the operation inside one SQL transaction:
// idempotency checks, wallet lock, movement, ledger, outbox events.
func (u *ProcessWager) Execute(ctx context.Context, in ProcessWagerInput) (ProcessWagerOutput, error) {
	hash, err := CanonicalHash(in.payload())
	if err != nil {
		return ProcessWagerOutput{}, err
	}
	if in.CorrelationID == "" {
		in.CorrelationID = uuid.NewString()
	}

	var out ProcessWagerOutput
	err = u.Tx.WithinTx(ctx, func(ctx context.Context) error {
		// 1. idempotency by key: same key + same payload = replay.
		existing, err := u.Transactions.GetByIdempotencyKey(ctx, in.IdempotencyKey)
		if err == nil {
			if existing.PayloadHash() != hash {
				return domain.ErrIdempotencyConflict
			}
			return u.fillReplay(ctx, existing, &out)
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		// 2. the same (provider, external) id must never be reapplied
		// under another key.
		if _, err := u.Transactions.GetByProviderExternal(ctx, in.ProviderID, in.ExternalTransactionID); err == nil {
			return domain.ErrIdempotencyConflict
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		// 3. lock the wallet row: writers on the same wallet serialize here.
		w, err := u.Wallets.GetForUpdate(ctx, in.WalletID)
		if err != nil {
			return err
		}

		// 4. build and process the domain transaction.
		tx, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
			ProviderID:                     in.ProviderID,
			ExternalTransactionID:          in.ExternalTransactionID,
			IdempotencyKey:                 in.IdempotencyKey,
			PayloadHash:                    hash,
			PlayerID:                       in.PlayerID,
			WalletID:                       in.WalletID,
			RoundID:                        in.RoundID,
			GameID:                         in.GameID,
			Kind:                           in.Kind,
			Money:                          in.Money,
			ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
		}, time.Now().UTC())
		if err != nil {
			return err
		}

		switch in.Kind {
		case domain.KindBet, domain.KindWin:
			err = u.applyBalanceOperation(ctx, tx, w)
		case domain.KindLoss:
			err = u.applyLoss(ctx, tx, w)
		case domain.KindRefund, domain.KindRollback:
			err = u.applyReversal(ctx, tx, w, in)
		default:
			err = fmt.Errorf("%w: unknown kind %q", domain.ErrInvalidInput, in.Kind)
		}
		if err != nil {
			return err
		}
		return u.fillOutput(ctx, tx, w, false, &out)
	})
	if err != nil {
		return ProcessWagerOutput{}, err
	}
	return out, nil
}

func (u *ProcessWager) fillOutput(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet, replay bool, out *ProcessWagerOutput) error {
	out.TransactionID = tx.ID()
	out.Status = tx.Status()
	out.Replay = replay
	out.FailureCode = tx.FailureCode()
	if tx.HasResultBalance() {
		out.Balance = tx.ResultBalance()
		return nil
	}
	// non-terminal or rejected without balance: report the current wallet state
	balance, err := u.Wallets.GetByID(ctx, w.ID())
	if err != nil {
		return err
	}
	out.Balance = balance.Balance()
	return nil
}

// fillReplay projects a persisted transaction without reapplying anything.
func (u *ProcessWager) fillReplay(ctx context.Context, existing *domain.WagerTransaction, out *ProcessWagerOutput) error {
	w, err := u.Wallets.GetByID(ctx, existing.WalletID())
	if err != nil {
		return err
	}
	out.TransactionID = existing.ID()
	out.Status = existing.Status()
	out.Replay = true
	out.FailureCode = existing.FailureCode()
	if existing.HasResultBalance() {
		out.Balance = existing.ResultBalance()
	} else {
		out.Balance = w.Balance()
	}
	return nil
}

// applyBalanceOperation handles BET (debit) and WIN (credit) synchronously.
func (u *ProcessWager) applyBalanceOperation(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet) error {
	if tx.Money().Currency() != w.Currency() {
		return u.rejectAndStore(ctx, tx, w, FailureCurrencyMismatch)
	}
	var entry domain.LedgerEntry
	var after domain.Money
	if tx.Kind() == domain.KindBet {
		d, err := w.Debit(tx.Money())
		if err != nil {
			if errors.Is(err, domain.ErrInsufficientFunds) {
				return u.rejectAndStore(ctx, tx, w, FailureInsufficientFunds)
			}
			return err
		}
		entry, err = domain.NewLedgerEntry(w.ID(), tx.ID(), domain.DebitDirection, tx.Money(), d.BalanceBefore, d.BalanceAfter)
		if err != nil {
			return err
		}
		after = d.BalanceAfter
	} else {
		c, err := w.Credit(tx.Money())
		if err != nil {
			return err
		}
		entry, err = domain.NewLedgerEntry(w.ID(), tx.ID(), domain.CreditDirection, tx.Money(), c.BalanceBefore, c.BalanceAfter)
		if err != nil {
			return err
		}
		after = c.BalanceAfter
	}

	if err := tx.MarkProcessed(after); err != nil {
		return err
	}
	if err := u.Transactions.Insert(ctx, tx); err != nil {
		return err
	}
	if err := u.Wallets.Update(ctx, w); err != nil {
		return err
	}
	if err := u.Ledger.Insert(ctx, entry); err != nil {
		return err
	}
	return u.emitProcessedAndBalanceEvents(ctx, tx, w, entry)
}

// applyLoss handles LOSS: zero amount, no ledger, no wallet change, but the
// operation is completed and produces WagerTransactionProcessed.
func (u *ProcessWager) applyLoss(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet) error {
	if tx.Money().Currency() != w.Currency() {
		return u.rejectAndStore(ctx, tx, w, FailureCurrencyMismatch)
	}
	if err := tx.MarkProcessed(w.Balance()); err != nil {
		return err
	}
	if err := u.Transactions.Insert(ctx, tx); err != nil {
		return err
	}
	return u.Outbox.Enqueue(ctx, domain.NewWagerTransactionProcessed(
		uuid.NewString(), tx.ID(), tx.ID(), tx.ProviderID(), tx.ExternalTransactionID(),
		tx.Kind(), tx.Money(), tx.ResultBalance(), time.Now().UTC(),
	))
}

// applyReversal handles REFUND and ROLLBACK. The reference must be resolvable
// and coherent; when it has not arrived yet the operation is stored as
// PENDING_REFERENCE and resumed later by the reference worker.
func (u *ProcessWager) applyReversal(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet, in ProcessWagerInput) error {
	ref, err := u.Transactions.GetByProviderExternal(ctx, in.ProviderID, in.ReferenceExternalTransactionID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		// reference not received yet: durable wait
		if err := tx.MarkPendingReference(); err != nil {
			return err
		}
		if err := u.Transactions.Insert(ctx, tx); err != nil {
			return err
		}
		return u.Outbox.Enqueue(ctx, domain.NewWagerTransactionPendingReference(
			uuid.NewString(), in.CorrelationID, tx.ID(), tx.ProviderID(), tx.ExternalTransactionID(),
			tx.Kind(), tx.ReferenceExternalTransactionID(), time.Now().UTC(),
		))
	}

	if code := referenceIssue(ref, w); code != "" {
		return u.rejectAndStore(ctx, tx, w, code)
	}

	entry, entryErr := reversalEntry(tx, ref, w)
	if entryErr != nil {
		if errors.Is(entryErr, domain.ErrInsufficientFunds) {
			return u.rejectAndStore(ctx, tx, w, FailureReversalExceedsBalance)
		}
		return entryErr
	}
	if err := tx.MarkProcessed(entry.BalanceAfter()); err != nil {
		return err
	}
	if err := u.Transactions.Insert(ctx, tx); err != nil {
		return err
	}
	if err := u.Wallets.Update(ctx, w); err != nil {
		return err
	}
	if err := u.Ledger.Insert(ctx, entry); err != nil {
		return err
	}
	return u.emitProcessedAndBalanceEvents(ctx, tx, w, entry)
}

// referenceIssue returns the rejection code for an unusable reference, or ""
// when the reference can be reversed.
func referenceIssue(ref *domain.WagerTransaction, w *domain.Wallet) string {
	switch ref.Status() {
	case domain.StatusProcessed:
		// usable
	case domain.StatusPending, domain.StatusPendingReference:
		return FailureReferencePending
	default:
		return FailureReferenceUnsuccessful
	}
	if ref.PlayerID() != w.PlayerID() || ref.WalletID() != w.ID() ||
		ref.Money().Currency() != w.Currency() {
		return FailureReferenceMismatch
	}
	return ""
}

// reversalEntry builds the ledger entry of the reversal movement, mutating the
// aggregate: REFUND always credits a referenced BET; ROLLBACK moves opposite
// to the original operation.
func reversalEntry(tx, ref *domain.WagerTransaction, w *domain.Wallet) (domain.LedgerEntry, error) {
	if ref.Money().Units() != tx.Money().Units() {
		return domain.LedgerEntry{}, fmt.Errorf("%w: reversal amount differs from the reference", domain.ErrReferenceMismatch)
	}
	var dir domain.Direction
	switch {
	case tx.Kind() == domain.KindRefund:
		if ref.Kind() != domain.KindBet {
			return domain.LedgerEntry{}, fmt.Errorf("%w: REFUND must reference a BET", domain.ErrReferenceMismatch)
		}
		dir = domain.CreditDirection
	case ref.Kind() == domain.KindBet:
		dir = domain.CreditDirection // ROLLBACK of a BET credits back
	case ref.Kind() == domain.KindWin, ref.Kind() == domain.KindRefund:
		dir = domain.DebitDirection // ROLLBACK takes back a credit
	default:
		return domain.LedgerEntry{}, fmt.Errorf("%w: ROLLBACK of %s is not supported", domain.ErrReferenceMismatch, ref.Kind())
	}

	var before, after domain.Money
	if dir == domain.CreditDirection {
		c, cerr := w.Credit(tx.Money())
		if cerr != nil {
			return domain.LedgerEntry{}, cerr
		}
		before, after = c.BalanceBefore, c.BalanceAfter
	} else {
		d, derr := w.Debit(tx.Money())
		if derr != nil {
			return domain.LedgerEntry{}, derr // ErrInsufficientFunds → REVERSAL_EXCEEDS_BALANCE
		}
		before, after = d.BalanceBefore, d.BalanceAfter
	}
	return domain.NewLedgerEntry(w.ID(), tx.ID(), dir, tx.Money(), before, after)
}

// rejectAndStore persists a confirmed business rejection with its stable code
// and emits the rejection event. Rejections are terminal and replayable.
func (u *ProcessWager) rejectAndStore(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet, failureCode string) error {
	if err := tx.MarkRejected(failureCode); err != nil {
		return err
	}
	if err := u.Transactions.Insert(ctx, tx); err != nil {
		return err
	}
	return u.Outbox.Enqueue(ctx, domain.NewWagerTransactionRejected(
		uuid.NewString(), tx.ID(), tx.ID(), tx.ProviderID(), tx.ExternalTransactionID(),
		tx.Kind(), failureCode, time.Now().UTC(),
	))
}

// emitProcessedAndBalanceEvents emits the completion event plus the balance
// change event (both originated by the same committed movement).
func (u *ProcessWager) emitProcessedAndBalanceEvents(ctx context.Context, tx *domain.WagerTransaction, w *domain.Wallet, entry domain.LedgerEntry) error {
	correlationID := tx.ID()
	if err := u.Outbox.Enqueue(ctx, domain.NewWagerTransactionProcessed(
		uuid.NewString(), correlationID, tx.ID(), tx.ProviderID(), tx.ExternalTransactionID(),
		tx.Kind(), tx.Money(), tx.ResultBalance(), time.Now().UTC(),
	)); err != nil {
		return err
	}
	return u.Outbox.Enqueue(ctx, domain.NewWalletBalanceChanged(
		uuid.NewString(), correlationID, w.ID(), tx.ID(),
		entry.Direction(), entry.Money(), entry.BalanceBefore(), entry.BalanceAfter(),
		w.Version(), time.Now().UTC(),
	))
}
