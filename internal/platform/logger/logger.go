// Package logger builds the application's structured JSON logger.
package logger

import (
	"log/slog"
	"os"
)

// New returns a JSON slog.Logger writing to stdout.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil))
}
