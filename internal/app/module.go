// Package app composes the applications with Uber Fx.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maaarkin/jungleplatform/internal/domain"
	"go.uber.org/fx"

	"github.com/maaarkin/jungleplatform/internal/platform/auth"
	"github.com/maaarkin/jungleplatform/internal/platform/config"
	"github.com/maaarkin/jungleplatform/internal/platform/httpserver"
	"github.com/maaarkin/jungleplatform/internal/platform/logger"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
	"github.com/maaarkin/jungleplatform/internal/transport/httpapi"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// New builds the API application.
func New(cfg config.Config) *fx.App {
	return fx.New(
		fx.Module("api",
			fx.Supply(cfg),
			fx.Provide(
				logger.New,
				newPool,
				fx.Annotate(postgres.NewTxManager, fx.As(new(domain.TxManager))),
				fx.Annotate(postgres.NewWalletRepo, fx.As(new(domain.WalletRepo))),
				fx.Annotate(postgres.NewTransactionRepo, fx.As(new(domain.TransactionRepo))),
				fx.Annotate(postgres.NewLedgerRepo, fx.As(new(domain.LedgerRepo))),
				fx.Annotate(postgres.NewInboxRepo, fx.As(new(domain.InboxRepo))),
				fx.Annotate(postgres.NewOutboxRepo, fx.As(new(domain.OutboxRepo))),
				fx.Annotate(postgres.NewReconstructor, fx.As(new(domain.Reconstructor))),
				usecase.NewOpenWallet,
				usecase.NewProcessWager,
				usecase.NewReconcile,
				newAuthenticator,

				newReadiness,
				httpapi.NewHealthHandler,
				httpapi.NewWalletsHandler,
				httpapi.NewTransactionsHandler,
				httpapi.NewQueriesHandler,
				newRouter,
				func(r *chi.Mux) http.Handler { return r },
				httpserver.New,
				newServeHook,
			),
			fx.Invoke(runServer),
		),
	)
}

// newPool opens the connection pool from configuration.
func newPool(cfg config.Config) (*pgxpool.Pool, error) {
	return postgres.NewPool(context.Background(), cfg.PostgresDSN)
}

// newAuthenticator builds the token authenticator from configuration.
func newAuthenticator(cfg config.Config) (*auth.Authenticator, error) {
	return auth.NewAuthenticator(cfg.KeycloakIssuerURL, cfg.KeycloakJWKSURL)
}

// newReadiness reports readiness: PostgreSQL reachable (SQS joins in Task 16).
func newReadiness(pool *pgxpool.Pool) httpapi.ReadinessFunc {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return pool.Ping(ctx)
	}
}

func newRouter(
	health *httpapi.HealthHandler,
	a *auth.Authenticator,
	wallets *httpapi.WalletsHandler,
	transactions *httpapi.TransactionsHandler,
	queries *httpapi.QueriesHandler,
) *chi.Mux {
	return httpapi.NewRouter(health, a.Middleware, wallets, transactions, queries)
}

// serveHook owns the context used to stop the server during shutdown.
type serveHook struct {
	cancel  context.CancelFunc
	done    chan error
	started bool
}

func newServeHook() *serveHook {
	return &serveHook{done: make(chan error, 1)}
}

type serveDeps struct {
	fx.In
	Lifecycle fx.Lifecycle
	Server    *http.Server
	Cfg       config.Config
	Log       *slog.Logger
	Hook      *serveHook
}

func runServer(d serveDeps) {
	d.Lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			d.Hook.cancel = cancel
			d.Hook.started = true
			go func() { d.Hook.done <- httpserver.Run(ctx, d.Server, d.Cfg, d.Log) }()
			return nil
		},
		OnStop: func(context.Context) error {
			if !d.Hook.started {
				return nil
			}
			d.Hook.cancel()
			return <-d.Hook.done
		},
	})
}
