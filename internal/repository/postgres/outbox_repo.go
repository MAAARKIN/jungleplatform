package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// claimLease is how long a claimed event stays invisible to other publishers
// before an abandoned claim may be re-claimed.
const claimLease = 60 * time.Second

// OutboxRepo implements domain.OutboxRepo with explicit SQL.
type OutboxRepo struct {
	pool *pgxpool.Pool
}

// NewOutboxRepo builds the repository over the given pool.
func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

// Enqueue stores the serialized event envelope in the same SQL transaction as
// the domain change that caused it.
func (r *OutboxRepo) Enqueue(ctx context.Context, e domain.Event) error {
	q := QuerierFor(ctx, r.pool)
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("postgres: outbox marshal: %w", err)
	}
	_, err = q.Exec(ctx,
		`INSERT INTO outbox (event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
		VALUES ($1::uuid, $2, $3, $4, $5, now())`,
		e.EventID, e.AggregateID, e.EventType, payload, e.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: outbox enqueue: %w", err)
	}
	return nil
}

// ClaimBatch claims up to limit pending events for one publisher using
// FOR UPDATE SKIP LOCKED: concurrent publishers never claim the same row and
// abandoned claims expire once next_attempt_at (the lease) has passed. It
// returns the raw serialized envelopes for publication.
func (r *OutboxRepo) ClaimBatch(ctx context.Context, publisherID string, limit int) ([]domain.OutboxMessage, error) {
	q := QuerierFor(ctx, r.pool)
	rows, err := q.Query(ctx,
		`WITH claimed AS (
			SELECT event_id FROM outbox
			WHERE published_at IS NULL AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox o
		SET claimed_by = $1, next_attempt_at = now() + make_interval(secs => $3)
		FROM claimed c
		WHERE o.event_id = c.event_id
		RETURNING o.event_id::text, o.payload`,
		publisherID, limit, claimLease.Seconds(),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: outbox claim: %w", err)
	}
	defer rows.Close()

	var out []domain.OutboxMessage
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, fmt.Errorf("postgres: outbox claim scan: %w", err)
		}
		out = append(out, domain.OutboxMessage{EventID: id, Payload: payload})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: outbox claim rows: %w", err)
	}
	return out, nil
}

// MarkPublished records the durable publication of an event.
func (r *OutboxRepo) MarkPublished(ctx context.Context, eventID string, at time.Time) error {
	q := QuerierFor(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE outbox SET published_at = $2 WHERE event_id = $1::uuid AND published_at IS NULL`,
		eventID, at,
	)
	if err != nil {
		return fmt.Errorf("postgres: outbox mark published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Reschedule requeues a failed event with a new next attempt time and attempt
// count (backoff is a publisher policy).
func (r *OutboxRepo) Reschedule(ctx context.Context, eventID string, next time.Time, attempts int) error {
	q := QuerierFor(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE outbox SET next_attempt_at = $2, attempts = $3, claimed_by = NULL
		WHERE event_id = $1::uuid AND published_at IS NULL`,
		eventID, next, attempts,
	)
	if err != nil {
		return fmt.Errorf("postgres: outbox reschedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
