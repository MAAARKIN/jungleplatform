// Package postgres implements the domain repository ports with pgx and
// explicit SQL. Every repository joins the caller's SQL transaction when one
// is active (see WithinTx) or falls back to the pool.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool and verifies connectivity with a ping.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}
