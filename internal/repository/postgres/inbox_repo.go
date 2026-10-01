package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// InboxRepo implements domain.InboxRepo with explicit SQL.
type InboxRepo struct {
	pool *pgxpool.Pool
}

// NewInboxRepo builds the repository over the given pool.
func NewInboxRepo(pool *pgxpool.Pool) *InboxRepo {
	return &InboxRepo{pool: pool}
}

// TryRegister registers the (consumer, messageId) pair durably. It returns
// true on first sight. On a re-delivery it verifies the payload hash: a
// different hash means the message content changed under the same identity,
// which is an anomaly and must not be silently processed.
func (r *InboxRepo) TryRegister(ctx context.Context, consumerName, messageID, payloadHash string) (bool, error) {
	q := QuerierFor(ctx, r.pool)
	var id string
	err := q.QueryRow(ctx,
		`INSERT INTO inbox (id, consumer_name, message_id, payload_hash, received_at)
		VALUES (gen_random_uuid(), $1, $2, $3, now())
		ON CONFLICT (consumer_name, message_id) DO NOTHING
		RETURNING id`,
		consumerName, messageID, payloadHash,
	).Scan(&id)
	if err == nil {
		return true, nil
	}
	// ON CONFLICT DO NOTHING suppresses the row, so pgx surfaces ErrNoRows —
	// that is the "already registered" case, not an error.
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("postgres: inbox register: %w", err)
	}
	var storedHash string
	if err := q.QueryRow(ctx,
		`SELECT payload_hash FROM inbox WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID,
	).Scan(&storedHash); err != nil {
		return false, fmt.Errorf("postgres: inbox lookup: %w", err)
	}
	if storedHash != payloadHash {
		return false, fmt.Errorf("%w: message %q seen with different payload", domain.ErrMessageHashMismatch, messageID)
	}
	return false, nil
}

// Complete marks the message as durably handled. It must run in the same SQL
// transaction as the domain changes.
func (r *InboxRepo) Complete(ctx context.Context, consumerName, messageID string) error {
	q := QuerierFor(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE inbox SET completed_at = now() WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID,
	)
	if err != nil {
		return fmt.Errorf("postgres: inbox complete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
