// Package outbox publishes pending integration events from the transactional
// outbox to the events queue. Publishers compete with FOR UPDATE SKIP LOCKED
// claims plus a lease, so multiple instances never publish the same event
// concurrently and abandoned claims are reclaimed after the lease expires.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// Publisher drains the outbox: claim → publish → mark published; on failure
// reschedule with exponential backoff (2^attempts seconds, capped at 60s).
type Publisher struct {
	pool     *pgxpool.Pool
	repo     domain.OutboxRepo
	client   *sqs.Client
	queueURL string
	name     string
	log      *slog.Logger
	interval time.Duration
}

// NewPublisher builds the publisher. interval is the polling tick (1s in
// production; shorter in tests).
func NewPublisher(
	pool *pgxpool.Pool,
	repo domain.OutboxRepo,
	client *sqs.Client,
	queueURL string,
	name string,
	log *slog.Logger,
	interval time.Duration,
) *Publisher {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = time.Second
	}
	return &Publisher{
		pool: pool, repo: repo, client: client, queueURL: queueURL,
		name: name, log: log, interval: interval,
	}
}

// Run publishes pending events until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	p.log.Info("outbox publisher started", "publisher", p.name, "queue", p.queueURL)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.log.Info("outbox publisher stopped")
			return nil
		case <-ticker.C:
			if err := p.publishBatch(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				p.log.Error("publish batch failed", "publisher", p.name, "error", err)
			}
		}
	}
}

func (p *Publisher) publishBatch(ctx context.Context) error {
	events, err := p.repo.ClaimBatch(ctx, p.name, 100)
	if err != nil {
		return fmt.Errorf("outbox: claim: %w", err)
	}
	for _, msg := range events {
		if ctx.Err() != nil {
			return nil
		}
		p.publish(ctx, msg)
	}
	return nil
}

// publish sends one event and confirms it durably. A failure reschedules the
// event with backoff; the claim lease covers a dead publisher in between.
func (p *Publisher) publish(ctx context.Context, msg domain.OutboxMessage) {
	started := time.Now()
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(msg.Payload)),
		MessageGroupId:         aws.String(msg.EventID), // FIFO ordering per event
		MessageDeduplicationId: aws.String(msg.EventID), // republishing preserves identity
	})
	if err != nil {
		next, attempts := p.backoff(ctx, msg.EventID)
		p.log.Error("publish failed, rescheduled",
			"publisher", p.name, "eventId", msg.EventID, "attempts", attempts,
			"nextAttempt", next, "error", err)
		return
	}
	if err := p.repo.MarkPublished(ctx, msg.EventID, time.Now().UTC()); err != nil {
		// the event is on the queue; the durable confirmation is best-effort
		// here — the lease-expiry reclaim plus the eventId deduplication id
		// keep consumers from double-applying
		p.log.Error("mark published failed (event already sent)",
			"publisher", p.name, "eventId", msg.EventID, "error", err)
		return
	}
	p.log.Info("event published", "publisher", p.name, "eventId", msg.EventID, "duration", time.Since(started))
}

// backoff reads the current attempts and reschedules with 2^attempts seconds
// capped at 60.
func (p *Publisher) backoff(ctx context.Context, eventID string) (time.Time, int) {
	// attempts are tracked by the repo update; reschedule with the incremented
	// count derived from the failed claim (claimed events carry their lease)
	var attempts int
	row := p.pool.QueryRow(ctx, `SELECT attempts FROM outbox WHERE event_id = $1::uuid`, eventID)
	_ = row.Scan(&attempts)
	if attempts < 1 {
		attempts = 1
	}
	next := time.Now().UTC().Add(time.Duration(min64(1<<attempts, 60)) * time.Second)
	if err := p.repo.Reschedule(ctx, eventID, next, attempts); err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			p.log.Error("reschedule failed", "publisher", p.name, "eventId", eventID, "error", err)
		}
	}
	return next, attempts
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
