// Command api runs the HTTP API service.
package main

import (
	"context"
	"log/slog"
	"os"

	_ "github.com/joho/godotenv/autoload"

	"github.com/maaarkin/jungleplatform/internal/app"
	"github.com/maaarkin/jungleplatform/internal/platform/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	app.New(cfg, func(context.Context) error { return nil }).Run()
}
