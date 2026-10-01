package domain

import (
	"context"
	"time"
)

// TxKey is the context key under which the active SQL transaction is stored
// while inside TxManager.WithinTx. Repositories read it to join the caller's
// transaction; outside a transaction they use the connection pool.
type TxKey struct{}

// TxManager delimits the SQL transaction boundary around repository calls.
// All financial mutations (wallet, transaction, ledger, inbox, outbox) must
// happen inside one WithinTx block so they commit atomically.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// WalletRepo persists and loads wallet aggregates.
type WalletRepo interface {
	Insert(ctx context.Context, w *Wallet) error
	GetByID(ctx context.Context, id string) (*Wallet, error)
	// GetForUpdate locks the wallet row (SELECT ... FOR UPDATE) so concurrent
	// writers to the same wallet serialize at the database.
	GetForUpdate(ctx context.Context, id string) (*Wallet, error)
	GetByPlayerAndCurrency(ctx context.Context, playerID string, c Currency) (*Wallet, error)
	Update(ctx context.Context, w *Wallet) error
}

// TransactionRepo persists and loads wager transactions.
type TransactionRepo interface {
	Insert(ctx context.Context, t *WagerTransaction) error
	GetByID(ctx context.Context, id string) (*WagerTransaction, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*WagerTransaction, error)
	GetByProviderExternal(ctx context.Context, providerID, externalTransactionID string) (*WagerTransaction, error)
	Update(ctx context.Context, t *WagerTransaction) error
	ListPendingReference(ctx context.Context, limit int) ([]*WagerTransaction, error)
}

// LedgerRepo appends and reads ledger entries. There is no Update or Delete:
// the ledger is append-only by contract and by database trigger.
type LedgerRepo interface {
	Insert(ctx context.Context, e LedgerEntry) error
	// ListByWallet pages in stable (created_at, id) order. The returned cursor
	// is opaque: pass it back to continue from the last entry.
	ListByWallet(ctx context.Context, walletID string, cursor string, limit int) ([]LedgerEntry, string, error)
}

// InboxRepo registers consumed SQS messages durably, sharing the SQL
// transaction of the domain changes.
type InboxRepo interface {
	// TryRegister returns true when (consumerName, messageID) is seen for the
	// first time; false means the message was already handled or is being
	// handled, and the hash must be verified before dropping it.
	TryRegister(ctx context.Context, consumerName, messageID, payloadHash string) (bool, error)
	Complete(ctx context.Context, consumerName, messageID string) error
}

// OutboxRepo stores integration events in the same SQL transaction as the
// domain changes that caused them, and hands them to competing publishers.
type OutboxRepo interface {
	Enqueue(ctx context.Context, e Event) error
	// ClaimBatch claims pending events for one publisher using a row lock
	// (FOR UPDATE SKIP LOCKED) and a lease on next_attempt_at, so abandoned
	// work is re-claimed by another instance after the lease expires.
	ClaimBatch(ctx context.Context, publisherID string, limit int) ([]Event, error)
	MarkPublished(ctx context.Context, eventID string, at time.Time) error
	// Reschedule requeues a failed event with exponential backoff.
	Reschedule(ctx context.Context, eventID string, next time.Time, attempts int) error
}

// Reconstructor rebuilds a wallet balance from the ledger for reconciliation.
type Reconstructor interface {
	// RebuildBalance returns the ledger-derived balance and the number of
	// entries summed. It never writes.
	RebuildBalance(ctx context.Context, walletID string) (Money, int, error)
}
