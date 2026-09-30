// Package app composes the application with Uber Fx.
package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"github.com/maaarkin/jungleplatform/internal/platform/config"
	"github.com/maaarkin/jungleplatform/internal/platform/httpserver"
	"github.com/maaarkin/jungleplatform/internal/platform/logger"
	"github.com/maaarkin/jungleplatform/internal/transport/httpapi"
)

// New builds the API application. ready is the injected readiness check.
func New(cfg config.Config, ready httpapi.ReadinessFunc) *fx.App {
	return fx.New(
		fx.Module("api",
			fx.Supply(cfg, ready),
			fx.Provide(
				logger.New,
				httpapi.NewHealthHandler,
				httpapi.NewRouter,
				func(r *chi.Mux) http.Handler { return r },
				httpserver.New,
				newServeHook,
			),
			fx.Invoke(runServer),
		),
	)
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
