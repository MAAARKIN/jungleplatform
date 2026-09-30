package httpapi

import "github.com/go-chi/chi/v5"

// NewRouter assembles the public HTTP routes.
func NewRouter(health *HealthHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Mount("/", health.Routes())
	return r
}
