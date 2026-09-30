// Package httpapi contains the HTTP transport layer handlers.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ReadinessFunc reports whether all backing dependencies are available.
// It returns nil when the service is ready to serve traffic.
type ReadinessFunc func(ctx context.Context) error

// HealthHandler serves the public health endpoints.
type HealthHandler struct {
	ready ReadinessFunc
}

// NewHealthHandler creates the handler with the given readiness check.
func NewHealthHandler(ready ReadinessFunc) *HealthHandler {
	return &HealthHandler{ready: ready}
}

// Routes returns the health routes mounted under /health.
func (h *HealthHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/health/live", h.live)
	r.Get("/health/ready", h.readyEndpoint)
	return r
}

func (h *HealthHandler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (h *HealthHandler) readyEndpoint(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
