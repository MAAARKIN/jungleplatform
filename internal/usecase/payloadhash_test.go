package usecase_test

import (
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

func betPayload(amount string) usecase.WagerPayload {
	m, _ := domain.ParseMoney(amount, domain.BRL)
	return usecase.WagerPayload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "tx-1",
		PlayerID:              "player-1",
		WalletID:              "wallet-1",
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 m,
	}
}

func TestCanonicalHashIsDeterministic(t *testing.T) {
	h1, err := usecase.CanonicalHash(betPayload("25.00"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := usecase.CanonicalHash(betPayload("25.00"))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Error("same payload must produce the same hash")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64 (sha256 hex)", len(h1))
	}
}

func TestCanonicalHashNormalizesMoneyScale(t *testing.T) {
	h1, _ := usecase.CanonicalHash(betPayload("25.00"))
	h2, _ := usecase.CanonicalHash(betPayload("25"))
	if h1 != h2 {
		t.Error("equivalent monetary values must hash equally (scale-2 normalization)")
	}
}

func TestCanonicalHashDistinguishesBusinessFields(t *testing.T) {
	base, _ := usecase.CanonicalHash(betPayload("25.00"))

	changed := betPayload("25.00")
	changed.RoundID = "round-2"
	other, _ := usecase.CanonicalHash(changed)
	if base == other {
		t.Error("changed business field must change the hash")
	}

	changed = betPayload("30.00")
	other, _ = usecase.CanonicalHash(changed)
	if base == other {
		t.Error("changed amount must change the hash")
	}
}

func TestCanonicalHashIgnoresKeyOrder(t *testing.T) {
	// raw maps with the same content in different insertion order hash equally
	m1 := map[string]any{"a": "1", "b": map[string]any{"x": "2", "y": "3"}}
	m2 := map[string]any{"b": map[string]any{"y": "3", "x": "2"}, "a": "1"}
	h1, err1 := usecase.CanonicalHash(m1)
	h2, err2 := usecase.CanonicalHash(m2)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if h1 != h2 {
		t.Error("key order must not affect the hash")
	}
}
