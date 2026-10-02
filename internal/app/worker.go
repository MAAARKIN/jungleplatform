package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/outbox"
	"github.com/maaarkin/jungleplatform/internal/pendingref"
	"github.com/maaarkin/jungleplatform/internal/platform/config"
	"github.com/maaarkin/jungleplatform/internal/platform/metrics"
	"github.com/maaarkin/jungleplatform/internal/transport/sqsconsumer"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// sqsConsumerName is shared by every worker instance so the inbox deduplicates
// redeliveries across processes.
const sqsConsumerName = "sqs-worker"

// NewWorker builds the background worker application (SQS consumer today;
// outbox publisher and reference worker join in the next tasks).
func NewWorker(cfg config.Config) *fx.App {
	return fx.New(
		fx.Module("worker",
			fx.Supply(cfg),
			fx.Provide(append(storageProviders(), workerProviders()...)...),
			fx.Invoke(runWorkers),
		),
	)
}

// newSQSClient builds the SQS client. A configured endpoint means LocalStack
// (local development), which accepts static dummy credentials; production
// uses the default AWS credential chain.
func newSQSClient(cfg config.Config) (*sqs.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion("us-east-1")}
	if cfg.SQSEndpoint != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(cfg.SQSEndpoint)
	}), nil
}

// workerProviders serve the background workers only.
func workerProviders() []any {
	return []any{
		metrics.New(prometheus.DefaultRegisterer),
		newSQSClient,
		newConsumer,
		newOutboxPublisher,
		newPendingRefWorker,
		newWorkerHook,
	}
}

// newPendingRefWorker builds the reference-resolution worker.
func newPendingRefWorker(
	pool *pgxpool.Pool,
	tx domain.TxManager,
	transactions domain.TransactionRepo,
	process *usecase.ProcessWager,
	cfg config.Config,
	log *slog.Logger,
) *pendingref.Worker {
	return pendingref.NewWorker(pool, tx, transactions, process, cfg.PendingRefTTL, 50, log, time.Second)
}

// newOutboxPublisher builds the competing publisher for the events queue.
func newOutboxPublisher(
	pool *pgxpool.Pool,
	repo domain.OutboxRepo,
	client *sqs.Client,
	cfg config.Config,
	log *slog.Logger,
	m *metrics.Registry,
) *outbox.Publisher {
	return outbox.NewPublisher(pool, repo, client, cfg.EventsQueueURL, "outbox-publisher", log, time.Second, m)
}

func newConsumer(
	client *sqs.Client,
	cfg config.Config,
	process *usecase.ProcessWager,
	inbox domain.InboxRepo,
	tx domain.TxManager,
	log *slog.Logger,
	m *metrics.Registry,
) *sqsconsumer.Consumer {
	return sqsconsumer.NewConsumer(client, cfg.SQSQueueURL, sqsConsumerName, process, inbox, tx, log, 10, m)
}

// workerHook owns the context that stops the workers on shutdown.
type workerHook struct {
	cancel  context.CancelFunc
	done    chan error
	started bool
}

func newWorkerHook() *workerHook {
	return &workerHook{done: make(chan error, 2)}
}

type workerDeps struct {
	fx.In
	Lifecycle fx.Lifecycle
	Hook      *workerHook
	Consumer  *sqsconsumer.Consumer
	Publisher *outbox.Publisher
	RefWorker *pendingref.Worker
}

func runWorkers(d workerDeps) {
	d.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			d.Hook.cancel = cancel
			d.Hook.started = true
			go func() { d.Hook.done <- d.Consumer.Run(ctx) }()
			go func() { d.Hook.done <- d.Publisher.Run(ctx) }()
			go func() { d.Hook.done <- d.RefWorker.Run(ctx) }()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if !d.Hook.started {
				return nil
			}
			d.Hook.cancel()
			select {
			case err := <-d.Hook.done:
				return err
			case <-time.After(30 * time.Second):
				return errors.New("workers did not stop within the grace period")
			}
		},
	})
}
