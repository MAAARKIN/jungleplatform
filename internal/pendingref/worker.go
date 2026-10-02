// Package pendingref resolves PENDING_REFERENCE wager transactions whose
// referenced operation arrived later. Retries follow a durable exponential
// backoff (next_reference_attempt_at); when the TTL or attempt budget is
// exhausted the operation is rejected with REFERENCE_NOT_FOUND.
package pendingref

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// Worker scans for due pending references and resumes them through the shared
// wager use case.
type Worker struct {
	pool         *pgxpool.Pool
	tx           domain.TxManager
	transactions domain.TransactionRepo
	process      *usecase.ProcessWager
	ttl          time.Duration
	maxAttempts  int
	log          *slog.Logger
	interval     time.Duration
}

// NewWorker builds the worker. ttl and maxAttempts bound how long a pending
// reference may wait before being rejected.
func NewWorker(
	pool *pgxpool.Pool,
	tx domain.TxManager,
	transactions domain.TransactionRepo,
	process *usecase.ProcessWager,
	ttl time.Duration,
	maxAttempts int,
	log *slog.Logger,
	interval time.Duration,
) *Worker {
	if log == nil {
		log = slog.Default()
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	if maxAttempts <= 0 {
		maxAttempts = 50
	}
	if interval <= 0 {
		interval = time.Second
	}
	return &Worker{
		pool: pool, tx: tx, transactions: transactions, process: process,
		ttl: ttl, maxAttempts: maxAttempts, log: log, interval: interval,
	}
}

// Run scans until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("pending reference worker started", "ttl", w.ttl, "maxAttempts", w.maxAttempts)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.log.Info("pending reference worker stopped")
			return nil
		case <-ticker.C:
			if err := w.scan(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				w.log.Error("pending reference scan failed", "error", err)
			}
		}
	}
}

func (w *Worker) scan(ctx context.Context) error {
	pending, err := w.transactions.ListPendingReference(ctx, 100)
	if err != nil {
		return fmt.Errorf("pendingref: list: %w", err)
	}
	for _, tx := range pending {
		if ctx.Err() != nil {
			return nil
		}
		if err := w.resolve(ctx, tx); err != nil {
			w.log.Error("reference resolution failed",
				"transactionId", tx.ID(), "error", err)
		}
	}
	return nil
}

// resolve attempts one transaction inside a transaction. The retry schedule
// is persisted in the same SQL transaction as any outcome.
func (w *Worker) resolve(ctx context.Context, tx *domain.WagerTransaction) error {
	err := w.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := w.process.Resume(ctx, tx); err != nil {
			if errors.Is(err, usecase.ErrReferenceNotYetAvailable) {
				return w.scheduleRetry(ctx, tx)
			}
			return err
		}
		return nil
	})
	if err == nil && tx.Status() != domain.StatusPendingReference {
		w.log.Info("pending reference resolved",
			"transactionId", tx.ID(), "status", tx.Status(), "providerId", tx.ProviderID())
	}
	return err
}

// scheduleRetry applies the exponential backoff policy. When the TTL or the
// attempt budget is exhausted, the operation is rejected with a stable code
// and the rejection event is emitted.
func (w *Worker) scheduleRetry(ctx context.Context, tx *domain.WagerTransaction) error {
	now := time.Now().UTC()
	if tx.ReferenceAttempts() >= w.maxAttempts || now.Sub(tx.CreatedAt()) > w.ttl {
		if err := tx.MarkRejected(usecase.FailureReferenceNotFound); err != nil {
			return err
		}
		if err := w.transactions.Update(ctx, tx); err != nil {
			return err
		}
		if err := w.process.EnqueueRejection(ctx, tx); err != nil {
			return err
		}
		w.log.Info("pending reference expired",
			"transactionId", tx.ID(), "attempts", tx.ReferenceAttempts(),
			"age", now.Sub(tx.CreatedAt()))
		return nil
	}
	attempts := tx.ReferenceAttempts() + 1
	next := now.Add(time.Duration(min64(1<<attempts, 60)) * time.Second)
	tx.ScheduleReferenceRetry(next, now)
	if err := w.transactions.Update(ctx, tx); err != nil {
		return err
	}
	w.log.Info("reference retry scheduled",
		"transactionId", tx.ID(), "attempts", attempts, "nextAttempt", next)
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
