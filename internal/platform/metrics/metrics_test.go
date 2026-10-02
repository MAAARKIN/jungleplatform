package metrics

import (
	"bytes"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObserveTransactionCounters(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.ObserveTransaction("PROCESSED", 5*time.Millisecond)
	r.ObserveTransaction("PROCESSED", 5*time.Millisecond)
	r.ObserveTransaction("REJECTED", time.Millisecond)

	if got := testutil.ToFloat64(r.TransactionsTotal.WithLabelValues("PROCESSED")); got != 2 {
		t.Errorf("PROCESSED total = %v, want 2", got)
	}
	if got := testutil.ToFloat64(r.TransactionsTotal.WithLabelValues("REJECTED")); got != 1 {
		t.Errorf("REJECTED total = %v, want 1", got)
	}
}

func TestDuplicateAndConflictCounters(t *testing.T) {
	r := New(prometheus.NewRegistry())
	r.DuplicatesTotal.Add(3)
	r.ConflictsTotal.Inc()
	if got := testutil.ToFloat64(r.DuplicatesTotal); got != 3 {
		t.Errorf("duplicates = %v", got)
	}
	if got := testutil.ToFloat64(r.ConflictsTotal); got != 1 {
		t.Errorf("conflicts = %v", got)
	}
}

func TestMetricsGatherFromOwnRegistry(t *testing.T) {
	reg := prometheus.NewRegistry()
	r := New(reg)
	r.OutboxPending.Set(7)
	if got := testutil.ToFloat64(r.OutboxPending); got != 7 {
		t.Errorf("outbox_pending = %v, want 7", got)
	}
	_ = bytes.NewBuffer
}
