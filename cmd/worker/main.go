// Command worker runs the background workers: SQS consumer, outbox publisher
// and pending reference resolver.
package main

import (
	"context"
	"log/slog"
	"os"

	"go.uber.org/fx"

	_ "github.com/joho/godotenv/autoload"

	"github.com/maaarkin/jungleplatform/internal/platform/config"
	"github.com/maaarkin/jungleplatform/internal/platform/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	app := fx.New(
		fx.Module("worker",
			fx.Supply(cfg),
			fx.Provide(logger.New),
			fx.Invoke(func(lc fx.Lifecycle, log *slog.Logger) {
				lc.Append(fx.Hook{
					OnStart: func(context.Context) error {
						log.Info("worker started")
						return nil
					},
				})
			}),
		),
	)
	app.Run()
}
