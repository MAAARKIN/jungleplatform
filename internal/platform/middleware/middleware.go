// Package middleware provides the chi middlewares: correlation id propagation
// and structured request logging without sensitive payloads.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/maaarkin/jungleplatform/internal/platform/auth"
)

type correlationKey struct{}

// Correlation propagates X-Correlation-Id or generates one per request.
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey{}, id)))
	})
}

// CorrelationID returns the request correlation id.
func CorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationKey{}).(string); ok {
		return id
	}
	return ""
}

// Logger logs one line per request with the traceable identifiers; never the
// financial payloads.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration", time.Since(started).String(),
				"correlationId", CorrelationID(r.Context()),
			}
			if claims, ok := auth.ClaimsFromContext(r.Context()); ok {
				attrs = append(attrs, "providerId", claims.ProviderID)
			}
			log.Info("http request", attrs...)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
