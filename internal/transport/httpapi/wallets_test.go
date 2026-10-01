//go:build integration

package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

type walletRequest struct {
	PlayerID       string       `json:"playerId"`
	InitialBalance domain.Money `json:"initialBalance"`
}

func TestOpenWalletCreatesWalletWithOpeningEvents(t *testing.T) {
	h, pool := newStack(t)
	token := tokenFor(t, "internal", "internal-secret")

	rec := postJSON(t, h, "/wallets", token, walletRequest{
		PlayerID:       "player-open-1",
		InitialBalance: mustMoneyDTO(t, "1000.00"),
	})
	if rec.Code != 201 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID       string `json:"id"`
		PlayerID string `json:"playerId"`
		Balance  domain.Money
		Version  int64 `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID == "" || resp.PlayerID != "player-open-1" || resp.Version != 1 {
		t.Errorf("body = %s", rec.Body.String())
	}
	if resp.Balance.Units() != 100000 || resp.Balance.Currency() != domain.BRL {
		t.Errorf("balance = %s", resp.Balance.String())
	}

	// one opening ledger entry and two outbox events in the same commit
	var entries, events int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM ledger_entries le JOIN wallets w ON w.id = le.wallet_id WHERE w.player_id = 'player-open-1'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Errorf("ledger entries = %d, want 1", entries)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Errorf("outbox events = %d, want 2 (processed + balance changed)", events)
	}
	var eventTypes []string
	rows, err := pool.Query(t.Context(), `SELECT event_type FROM outbox ORDER BY event_type`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var et string
		if err := rows.Scan(&et); err != nil {
			t.Fatal(err)
		}
		eventTypes = append(eventTypes, et)
	}
	rows.Close()
	if strings.Join(eventTypes, ",") != "WagerTransactionProcessed,WalletBalanceChanged" {
		t.Errorf("event types = %v", eventTypes)
	}
}

func TestOpenWalletZeroBalanceSkipsFinancialRecords(t *testing.T) {
	h, pool := newStack(t)
	token := tokenFor(t, "internal", "internal-secret")

	rec := postJSON(t, h, "/wallets", token, walletRequest{
		PlayerID:       "player-open-zero",
		InitialBalance: mustMoneyDTO(t, "0.00"),
	})
	if rec.Code != 201 {
		t.Fatalf("status = %d", rec.Code)
	}
	var entries, events, transactions int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM ledger_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM wager_transactions`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if entries != 0 || events != 0 || transactions != 0 {
		t.Errorf("zero opening must not create financial records: %d %d %d", entries, events, transactions)
	}
}

func TestOpenWalletDuplicatePlayerCurrencyConflicts(t *testing.T) {
	h, _ := newStack(t)
	token := tokenFor(t, "internal", "internal-secret")

	first := postJSON(t, h, "/wallets", token, walletRequest{
		PlayerID:       "player-open-dup",
		InitialBalance: mustMoneyDTO(t, "50.00"),
	})
	if first.Code != 201 {
		t.Fatalf("first status = %d", first.Code)
	}
	second := postJSON(t, h, "/wallets", token, walletRequest{
		PlayerID:       "player-open-dup",
		InitialBalance: mustMoneyDTO(t, "50.00"),
	})
	if second.Code != 409 {
		t.Errorf("duplicate status = %d, want 409", second.Code)
	}
}

func TestOpenWalletRequiresInternalIdentity(t *testing.T) {
	h, _ := newStack(t)
	providerToken := tokenFor(t, "provider-a", "provider-a-secret")

	rec := postJSON(t, h, "/wallets", providerToken, walletRequest{
		PlayerID:       "player-open-forbidden",
		InitialBalance: mustMoneyDTO(t, "10.00"),
	})
	if rec.Code != 403 {
		t.Errorf("provider opening wallet status = %d, want 403", rec.Code)
	}

	noToken := postJSON(t, h, "/wallets", "", walletRequest{
		PlayerID:       "player-open-anon",
		InitialBalance: mustMoneyDTO(t, "10.00"),
	})
	if noToken.Code != 401 {
		t.Errorf("unauthenticated status = %d, want 401", noToken.Code)
	}
}

func TestOpenWalletInvalidInputRejected(t *testing.T) {
	h, _ := newStack(t)
	token := tokenFor(t, "internal", "internal-secret")

	rec := postJSON(t, h, "/wallets", token, map[string]any{
		"playerId":       "player-open-invalid",
		"initialBalance": map[string]any{"amount": "1e5", "currency": "BRL"},
	})
	if rec.Code != 400 {
		t.Errorf("scientific notation status = %d, want 400", rec.Code)
	}

	rec = postJSON(t, h, "/wallets", token, map[string]any{
		"playerId":       "",
		"initialBalance": map[string]any{"amount": "10.00", "currency": "BRL"},
	})
	if rec.Code != 400 {
		t.Errorf("empty player status = %d, want 400", rec.Code)
	}
}

func mustMoneyDTO(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, domain.BRL)
	if err != nil {
		t.Fatalf("ParseMoney(%s): %v", amount, err)
	}
	return m
}
