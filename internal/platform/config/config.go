// Package config loads application configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds all application settings. Values come from the environment.
type Config struct {
	HTTPAddr             string
	PostgresDSN          string
	SQSEndpoint          string
	SQSQueueURL          string
	SQSDLQURL            string
	KeycloakIssuerURL    string
	KeycloakJWKSURL      string
	KeycloakClientID     string
	KeycloakClientSecret string
	WorkerMode           string
	ShutdownGrace        time.Duration
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Load reads configuration from the environment and validates required values.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:             env("HTTP_ADDR", ":8080"),
		PostgresDSN:          os.Getenv("POSTGRES_DSN"),
		SQSEndpoint:          os.Getenv("SQS_ENDPOINT"),
		SQSQueueURL:          os.Getenv("SQS_QUEUE_URL"),
		SQSDLQURL:            os.Getenv("SQS_DLQ_URL"),
		KeycloakIssuerURL:    os.Getenv("KEYCLOAK_ISSUER_URL"),
		KeycloakJWKSURL:      os.Getenv("KEYCLOAK_JWKS_URL"),
		KeycloakClientID:     os.Getenv("KEYCLOAK_CLIENT_ID"),
		KeycloakClientSecret: os.Getenv("KEYCLOAK_CLIENT_SECRET"),
		WorkerMode:           env("WORKER_MODE", "worker"),
		ShutdownGrace:        5 * time.Second,
	}

	required := map[string]string{
		"POSTGRES_DSN":           cfg.PostgresDSN,
		"SQS_ENDPOINT":           cfg.SQSEndpoint,
		"SQS_QUEUE_URL":          cfg.SQSQueueURL,
		"SQS_DLQ_URL":            cfg.SQSDLQURL,
		"KEYCLOAK_ISSUER_URL":    cfg.KeycloakIssuerURL,
		"KEYCLOAK_CLIENT_ID":     cfg.KeycloakClientID,
		"KEYCLOAK_CLIENT_SECRET": cfg.KeycloakClientSecret,
	}
	for name, value := range required {
		if value == "" {
			return Config{}, fmt.Errorf("config: required environment variable %s is not set", name)
		}
	}

	if raw := os.Getenv("SHUTDOWN_GRACE"); raw != "" {
		g, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("config: invalid SHUTDOWN_GRACE %q: %w", raw, err)
		}
		cfg.ShutdownGrace = g
	}
	return cfg, nil
}
