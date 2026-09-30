// Package httpserver provides the lifecycle-managed HTTP server.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/maaarkin/jungleplatform/internal/platform/config"
)

// New builds an *http.Server bound to cfg.HTTPAddr serving handler.
func New(cfg config.Config, log *slog.Logger, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
}

// Run blocks until ctx is cancelled, then shuts the server down within
// ShutdownGrace. It returns nil on graceful shutdown.
func Run(ctx context.Context, srv *http.Server, cfg config.Config, log *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("httpserver: listen %s: %w", srv.Addr, err)
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	log.Info("http server started", "addr", srv.Addr)
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return fmt.Errorf("httpserver: serve: %w", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("httpserver: shutdown: %w", err)
	}
	log.Info("http server stopped")
	return nil
}
