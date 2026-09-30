package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func TestWalletBalanceChangedEvent(t *testing.T) {
	e := domain.NewWalletBalanceChanged(
		"event-1",
		"corr-1",
		"wallet-1",
		"tx-1",
		domain.DebitDirection,
		mustMoney(t, 2500, domain.BRL),
		mustMoney(t, 100000, domain.BRL),
		mustMoney(t, 97500, domain.BRL),
		2,
		fixedNow,
	)

	if e.EventType != domain.EventTypeWalletBalanceChanged {
		t.Errorf("eventType = %s", e.EventType)
	}
	if e.AggregateID != "wallet-1" {
		t.Errorf("aggregateId = %s", e.AggregateID)
	}
	if e.Version != 1 {
		t.Errorf("envelope version = %d, want 1 (event schema version)", e.Version)
	}

	data := e.Data.(domain.WalletBalanceChangedData)
	if data.WalletID != "wallet-1" || data.TransactionID != "tx-1" {
		t.Error("payload identity lost")
	}
	if data.Direction != domain.DebitDirection {
		t.Errorf("direction = %s", data.Direction)
	}
	if data.Money.Units() != 2500 || data.BalanceBefore.Units() != 100000 || data.BalanceAfter.Units() != 97500 {
		t.Error("payload money lost")
	}
	if data.WalletVersion != 2 {
		t.Errorf("walletVersion = %d", data.WalletVersion)
	}
}

func TestEventEnvelopeJSON(t *testing.T) {
	e := domain.NewWalletBalanceChanged(
		"event-1", "corr-1", "wallet-1", "tx-1",
		domain.DebitDirection,
		mustMoney(t, 2500, domain.BRL),
		mustMoney(t, 100000, domain.BRL),
		mustMoney(t, 97500, domain.BRL),
		2,
		fixedNow,
	)

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, field := range []string{
		`"eventId":"event-1"`, `"eventType":"WalletBalanceChanged"`,
		`"aggregateId":"wallet-1"`, `"correlationId":"corr-1"`,
		`"occurredAt":"2026-09-30T12:00:00Z"`, `"version":1`, `"data":`, `"walletVersion":2`,
	} {
		if !strings.Contains(s, field) {
			t.Errorf("envelope missing %s in %s", field, s)
		}
	}
}

func TestOccurredAtIsUTC(t *testing.T) {
	local := time.Date(2026, 9, 30, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	e := domain.NewWagerTransactionProcessed("e1", "c1", "tx-1", "provider-a", "tx-1", domain.KindBet, mustMoney(t, 2500, domain.BRL), mustMoney(t, 97500, domain.BRL), local)
	if e.OccurredAt.Location() != time.UTC {
		t.Errorf("occurredAt = %s, want UTC", e.OccurredAt)
	}
}

func TestWagerTransactionProcessedEvent(t *testing.T) {
	e := domain.NewWagerTransactionProcessed("e1", "c1", "tx-1", "provider-a", "tx-1", domain.KindBet, mustMoney(t, 2500, domain.BRL), mustMoney(t, 97500, domain.BRL), fixedNow)
	if e.EventType != domain.EventTypeWagerTransactionProcessed {
		t.Errorf("eventType = %s", e.EventType)
	}
	if e.AggregateID != "tx-1" {
		t.Errorf("aggregateId = %s", e.AggregateID)
	}
	data := e.Data.(domain.WagerTransactionProcessedData)
	if data.ProviderID != "provider-a" || data.ExternalTransactionID != "tx-1" || data.Kind != domain.KindBet {
		t.Error("payload fields lost")
	}
	if data.Money.Units() != 2500 || data.ResultBalance.Units() != 97500 {
		t.Error("payload money lost")
	}
}

func TestWagerTransactionRejectedEvent(t *testing.T) {
	e := domain.NewWagerTransactionRejected("e1", "c1", "tx-1", "provider-a", "tx-1", domain.KindBet, "INSUFFICIENT_FUNDS", fixedNow)
	if e.EventType != domain.EventTypeWagerTransactionRejected {
		t.Errorf("eventType = %s", e.EventType)
	}
	data := e.Data.(domain.WagerTransactionRejectedData)
	if data.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("failureCode = %s", data.FailureCode)
	}
}

func TestPendingReferenceEvent(t *testing.T) {
	e := domain.NewWagerTransactionPendingReference("e1", "c1", "tx-1", "provider-a", "tx-1", domain.KindRefund, "tx-0", fixedNow)
	if e.EventType != domain.EventTypeWagerTransactionPendingReference {
		t.Errorf("eventType = %s", e.EventType)
	}
	data := e.Data.(domain.WagerTransactionPendingReferenceData)
	if data.ReferenceExternalTransactionID != "tx-0" {
		t.Errorf("reference = %s", data.ReferenceExternalTransactionID)
	}
}

func TestEventIDsAreFixedByConstructor(t *testing.T) {
	e1 := domain.NewWagerTransactionProcessed("e1", "c1", "tx-1", "p", "x", domain.KindBet, mustMoney(t, 1, domain.BRL), mustMoney(t, 1, domain.BRL), fixedNow)
	e2 := domain.NewWagerTransactionProcessed("e1", "c1", "tx-1", "p", "x", domain.KindBet, mustMoney(t, 1, domain.BRL), mustMoney(t, 1, domain.BRL), fixedNow)
	b1, _ := json.Marshal(e1)
	b2, _ := json.Marshal(e2)
	if string(b1) != string(b2) {
		t.Error("same inputs must produce identical serialized events (deterministic payload)")
	}
}

func TestBalanceChangedRequiresMatchingEventAndWalletIDs(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("empty eventId must be rejected")
		}
	}()
	domain.NewWalletBalanceChanged("", "c1", "w", "tx", domain.DebitDirection, mustMoney(t, 1, domain.BRL), mustMoney(t, 1, domain.BRL), mustMoney(t, 1, domain.BRL), 1, fixedNow)
}
