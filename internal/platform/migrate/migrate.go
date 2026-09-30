// Package migrate applies versioned SQL migrations using golang-migrate.
package migrate

import (
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Run applies (or reverts) all migrations from fsys against dsn.
// direction must be "up" or "down".
func Run(dsn string, fsys fs.FS, direction string) error {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("migrate: invalid direction %q (use up or down)", direction)
	}
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrate: load migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return fmt.Errorf("migrate: create instance: %w", err)
	}
	defer m.Close()
	if direction == "up" {
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("migrate: up: %w", err)
		}
		return nil
	}
	if err := m.Down(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate: down: %w", err)
	}
	return nil
}
