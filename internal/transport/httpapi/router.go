package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter assembles all HTTP routes: public health endpoints and
// authenticated business endpoints.
func NewRouter(health *HealthHandler, authenticated func(http.Handler) http.Handler, handlers ...Registrable) *chi.Mux {
	r := chi.NewRouter()
	health.Register(r)
	r.Group(func(gr chi.Router) {
		gr.Use(authenticated)
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
