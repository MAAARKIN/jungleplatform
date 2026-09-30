package config_test

import (
	"testing"
	"time"

	"github.com/maaarkin/jungleplatform/internal/platform/config"
)

func setEnvs(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, e := range []string{
		"HTTP_ADDR", "POSTGRES_DSN", "SQS_ENDPOINT", "SQS_QUEUE_URL", "SQS_DLQ_URL",
		"KEYCLOAK_ISSUER_URL", "KEYCLOAK_CLIENT_ID", "KEYCLOAK_CLIENT_SECRET",
		"WORKER_MODE", "SHUTDOWN_GRACE",
	} {
		t.Setenv(e, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnvs() map[string]string {
	return map[string]string{
		"POSTGRES_DSN":           "postgres://jungle:jungle@localhost:5432/jungle",
		"SQS_ENDPOINT":           "http://localhost:4566",
		"SQS_QUEUE_URL":          "http://localhost:4566/000000000000/wager-transactions.fifo",
		"SQS_DLQ_URL":            "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		"KEYCLOAK_ISSUER_URL":    "http://localhost:8180/realms/jungle",
		"KEYCLOAK_CLIENT_ID":     "provider-a",
		"KEYCLOAK_CLIENT_SECRET": "secret",
	}
}

func TestLoadDefaults(t *testing.T) {
	setEnvs(t, validEnvs())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr default = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.ShutdownGrace != 5*time.Second {
		t.Errorf("ShutdownGrace default = %v, want 5s", cfg.ShutdownGrace)
	}
	if cfg.WorkerMode != "worker" {
		t.Errorf("WorkerMode default = %q, want %q", cfg.WorkerMode, "worker")
	}
}

func TestLoadOverrides(t *testing.T) {
	setEnvs(t, map[string]string{
		"HTTP_ADDR": ":9090", "SHUTDOWN_GRACE": "10s", "WORKER_MODE": "api",
		"POSTGRES_DSN": "postgres://x", "SQS_ENDPOINT": "http://sqs",
		"SQS_QUEUE_URL": "q", "SQS_DLQ_URL": "d",
		"KEYCLOAK_ISSUER_URL": "http://kc", "KEYCLOAK_CLIENT_ID": "c",
		"KEYCLOAK_CLIENT_SECRET": "s",
	})
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":9090" || cfg.ShutdownGrace != 10*time.Second || cfg.WorkerMode != "api" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadRejectsMissingRequired(t *testing.T) {
	for _, missing := range []string{
		"POSTGRES_DSN", "SQS_ENDPOINT", "SQS_QUEUE_URL", "SQS_DLQ_URL",
		"KEYCLOAK_ISSUER_URL", "KEYCLOAK_CLIENT_ID", "KEYCLOAK_CLIENT_SECRET",
	} {
		envs := validEnvs()
		delete(envs, missing)
		setEnvs(t, envs)
		if _, err := config.Load(); err == nil {
			t.Errorf("expected error for missing %s", missing)
		}
	}
}

func TestLoadRejectsInvalidGrace(t *testing.T) {
	setEnvs(t, validEnvs())
	t.Setenv("SHUTDOWN_GRACE", "not-a-duration")
	if _, err := config.Load(); err == nil {
		t.Error("expected error for invalid SHUTDOWN_GRACE")
	}
}
