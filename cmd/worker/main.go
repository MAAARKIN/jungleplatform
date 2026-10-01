// Command worker runs the background workers: SQS consumer, outbox publisher
// and pending reference resolver.
package main

import (
	"log/slog"
	"os"

	"github.com/maaarkin/jungleplatform/internal/app"
	"github.com/maaarkin/jungleplatform/internal/platform/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	app.NewWorker(cfg).Run()
}
