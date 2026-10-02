package sqsconsumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/metrics"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// Consumer polls the queue and processes messages with the shared wager use
// case. Contract: inbox registration, domain changes and outbox events share
// one SQL transaction; the SQS message is deleted only after that commit.
// Confirmed business rejections are terminal and also delete the message.
// Transient failures keep the message for redelivery; exhausted redeliveries
// reach the DLQ through the queue's redrive policy.
type Consumer struct {
	client      *sqs.Client
	queueURL    string
	name        string
	process     *usecase.ProcessWager
	inbox       domain.InboxRepo
	tx          domain.TxManager
	log         *slog.Logger
	waitSeconds int
	metrics     *metrics.Registry
}

// NewConsumer builds the consumer. waitSeconds is the long-poll interval in
// seconds (production uses 10; tests use 1).
func NewConsumer(
	client *sqs.Client,
	queueURL string,
	name string,
	process *usecase.ProcessWager,
	inbox domain.InboxRepo,
	tx domain.TxManager,
	log *slog.Logger,
	waitSeconds int,
	metricsReg *metrics.Registry,
) *Consumer {
	if log == nil {
		log = slog.Default()
	}
	if waitSeconds <= 0 {
		waitSeconds = 10
	}
	return &Consumer{
		client: client, queueURL: queueURL, name: name,
		process: process, inbox: inbox, tx: tx, log: log, waitSeconds: waitSeconds,
		metrics: metricsReg,
	}
}

// Run polls until ctx is cancelled. On SIGTERM the caller cancels the context;
// the in-flight message completes (or fails) within the queue visibility
// timeout, and unconsumed messages become visible again for safe redelivery.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("sqs consumer started", "queue", c.queueURL, "consumer", c.name)
	for {
		select {
		case <-ctx.Done():
			c.log.Info("sqs consumer stopped")
			return nil
		default:
		}
		if err := c.poll(ctx); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			c.log.Error("poll failed; backing off", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (c *Consumer) poll(ctx context.Context) error {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(c.queueURL),
		MaxNumberOfMessages:   10,
		WaitTimeSeconds:       int32(c.waitSeconds),
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return fmt.Errorf("sqs: receive: %w", err)
	}
	for _, msg := range out.Messages {
		if ctx.Err() != nil {
			return nil
		}
		c.handle(ctx, msg)
	}
	return nil
}

// handle processes one message and deletes it on confirmed outcomes.
func (c *Consumer) handle(ctx context.Context, msg types.Message) {
	raw := aws.ToString(msg.Body)
	receipt := aws.ToString(msg.ReceiptHandle)

	var envelope Envelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil || !envelope.valid() {
		// invalid payload: transient handling lets the redrive policy route it
		// to the DLQ after maxReceiveCount
		c.log.Error("invalid message, leaving for redrive", "messageId", msg.MessageId)
		if c.metrics != nil {
			c.metrics.DLQTotal.Inc()
		}
		return
	}

	hash := sha256.Sum256([]byte(raw))
	started := time.Now()
	err := c.tx.WithinTx(ctx, func(ctx context.Context) error {
		first, err := c.inbox.TryRegister(ctx, c.name, envelope.MessageID, hex.EncodeToString(hash[:]))
		if err != nil {
			return err
		}
		if !first {
			// duplicate delivery: the hash matched, the message is already
			// durably handled — safe to remove
			if c.metrics != nil {
				c.metrics.DuplicatesTotal.Inc()
			}
			return nil
		}
		out, err := c.process.Execute(ctx, envelope.Data.toInput())
		if err != nil {
			return err
		}
		c.log.Info("message processed",
			"messageId", envelope.MessageID,
			"transactionId", out.TransactionID,
			"status", out.Status,
			"providerId", envelope.Data.ProviderID,
			"walletId", envelope.Data.WalletID,
		)
		return c.inbox.Complete(ctx, c.name, envelope.MessageID)
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageHashMismatch) {
			// same id, different content: anomaly, do not delete
			c.log.Error("message hash mismatch, leaving for redrive", "messageId", envelope.MessageID)
			return
		}
		c.log.Error("processing failed, leaving for redelivery",
			"messageId", envelope.MessageID, "error", err)
		if c.metrics != nil {
			c.metrics.RetriesTotal.Inc()
		}
		return
	}

	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: aws.String(receipt),
	}); err != nil {
		c.log.Error("delete failed; message will be redelivered (inbox protects against reprocessing)",
			"messageId", envelope.MessageID, "error", err)
		return
	}
	c.log.Debug("message deleted", "messageId", envelope.MessageID, "duration", time.Since(started))
}

// toInput projects the envelope payload into the shared use case input.
func (d Data) toInput() usecase.ProcessWagerInput {
	return usecase.ProcessWagerInput{
		ProviderID:                     d.ProviderID,
		ExternalTransactionID:          d.ExternalTransactionID,
		IdempotencyKey:                 d.IdempotencyKey,
		PlayerID:                       d.PlayerID,
		WalletID:                       d.WalletID,
		RoundID:                        d.RoundID,
		GameID:                         d.GameID,
		Kind:                           domain.Kind(d.Kind),
		Money:                          d.Money,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
		CorrelationID:                  uuid.NewString(),
	}
}
