package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewRouter assembles all HTTP routes: public health and metrics endpoints
// and authenticated business endpoints.
func NewRouter(
	health *HealthHandler,
	correlation func(http.Handler) http.Handler,
	requestLog func(http.Handler) http.Handler,
	authenticated func(http.Handler) http.Handler,
	handlers ...Registrable,
) *chi.Mux {
	r := chi.NewRouter()
	r.Use(correlation)
	health.Register(r)
	r.Get("/metrics", promhttp.Handler().ServeHTTP)
	r.Group(func(gr chi.Router) {
		gr.Use(authenticated)
		// the request logger runs inside authentication so it sees the
		// provider identity for the traceable fields
		gr.Use(requestLog)
		for _, h := range handlers {
			h.Register(gr)
		}
	})
	return r
}

// Registrable is any handler that mounts its endpoints on a router.
type Registrable interface {
	Register(r chi.Router)
}
