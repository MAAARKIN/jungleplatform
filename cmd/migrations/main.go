// Command migrations applies or reverts the database migrations.
// Usage: migrations [-dsn <postgres dsn>] [-direction up|down]
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/joho/godotenv/autoload"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("POSTGRES_DSN"), "postgres DSN (defaults to POSTGRES_DSN)")
	direction := flag.String("direction", "up", "migration direction: up or down")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "migrations: POSTGRES_DSN is required")
		os.Exit(1)
	}
	if err := migrate.Run(*dsn, migrations.FS, *direction); err != nil {
		slog.Error("migrations failed", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied", "direction", *direction)
}
