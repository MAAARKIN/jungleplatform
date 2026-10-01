package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// Querier is the execution surface shared by pool and transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// QuerierFor returns the active transaction stored in ctx (via WithinTx), or
// the pool when no transaction is active.
func QuerierFor(ctx context.Context, pool *pgxpool.Pool) Querier {
	if tx, ok := ctx.Value(domain.TxKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// WithinTx delimits one SQL transaction. Repositories read the transaction
// from ctx (domain.TxKey), so all financial changes inside fn commit — or
// roll back — atomically.
func WithinTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	txCtx := context.WithValue(ctx, domain.TxKey{}, tx)
	if err := fn(txCtx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return errors.Join(fmt.Errorf("postgres: rollback after %w", err), rbErr)
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a unique-constraint violation for
// the given constraint name (or any, when name is empty).
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
