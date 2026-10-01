//go:build integration

package migrate_test

import (
	"io/fs"
	"os"
	"testing"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
)

const testDSN = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"

func dsn(t *testing.T) string {
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return testDSN
}

func TestMigrateUpDownUp(t *testing.T) {
	f := migrationsFS(t)
	if err := migrate.Run(dsn(t), f, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrate.Run(dsn(t), f, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := migrate.Run(dsn(t), f, "up"); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestMigrateRejectsBadDirection(t *testing.T) {
	f := migrationsFS(t)
	if err := migrate.Run(dsn(t), f, "sideways"); err == nil {
		t.Error("expected error for invalid direction")
	}
}

func migrationsFS(t *testing.T) fs.FS {
	t.Helper()
	f, err := fs.Sub(migrations.FS, ".")
	if err != nil {
		t.Fatalf("sub fs: %v", err)
	}
	return f
}
