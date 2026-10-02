// Package metrics centralizes the Prometheus instrumentation of the financial
// flows. Counters and gauges are exported on GET /metrics by the API and on a
// dedicated listener by the worker.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Registry holds every application metric.
type Registry struct {
	TransactionsTotal   *prometheus.CounterVec
	DuplicatesTotal     prometheus.Counter
	ConflictsTotal      prometheus.Counter
	RetriesTotal        prometheus.Counter
	DLQTotal            prometheus.Counter
	OutboxPending       prometheus.Gauge
	ProcessingDuration  *prometheus.HistogramVec
	ReconciliationDiver prometheus.Counter
}

// New builds the registry with all challenge-required metrics, registering
// on the given registerer (the default registry in production).
func New(reg prometheus.Registerer) *Registry {
	factory := promauto.With(reg)
	return &Registry{
		TransactionsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_transactions_total",
			Help: "Wager transactions by final status.",
		}, []string{"status"}),
		DuplicatesTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "duplicates_total",
			Help: "Duplicate deliveries absorbed by the inbox.",
		}),
		ConflictsTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "idempotency_conflicts_total",
			Help: "Idempotency key payload conflicts.",
		}),
		RetriesTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "retries_total",
			Help: "Retryable failures (transient processing, publish reschedules).",
		}),
		DLQTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "dlq_total",
			Help: "Messages left for the redrive policy (invalid or exhausted).",
		}),
		OutboxPending: factory.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_pending",
			Help: "Outbox events not yet published.",
		}),
		ProcessingDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "processing_duration_seconds",
			Help:    "End-to-end processing latency of wager operations.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
		}, []string{"outcome"}),
		ReconciliationDiver: factory.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliation checks that found a stored/calculated divergence.",
		}),
	}
}

// ObserveTransaction counts one transaction outcome and its latency.
func (r *Registry) ObserveTransaction(status string, d time.Duration) {
	r.TransactionsTotal.WithLabelValues(status).Inc()
	r.ProcessingDuration.WithLabelValues(status).Observe(d.Seconds())
}
