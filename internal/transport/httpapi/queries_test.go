//go:build integration

package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func TestWalletReadLedgerAndReconciliationFlow(t *testing.T) {
	h, pool := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")

	// open 100.00 and bet 25.00 (provider-a), producing 2 ledger entries
	rec := postJSON(t, h, "/wallets", internal, walletRequest{
		PlayerID:       "player-query-1",
		InitialBalance: mustMoneyDTO(t, "100.00"),
	})
	if rec.Code != 201 {
		t.Fatalf("open: %d %s", rec.Code, rec.Body.String())
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}

	bet := wagerBody("provider-a", "tx-q-1", "BET", "25.00")
	bet["walletId"] = opened.ID
	if rec := postWager(t, h, providerA, "provider-a:tx-q-1", bet); rec.Code != 200 {
		t.Fatalf("bet: %d %s", rec.Code, rec.Body.String())
	}

	// GET wallet → internal only; provider gets 403
	if rec := requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID, internal, nil, nil); rec.Code != 200 {
		t.Fatalf("get wallet: %d", rec.Code)
	}
	var wallet struct {
		Balance struct {
			Amount string `json:"amount"`
		} `json:"balance"`
		Version int64 `json:"version"`
	}
	rec = requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID, internal, nil, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &wallet); err != nil {
		t.Fatal(err)
	}
	if wallet.Balance.Amount != "75.00" || wallet.Version != 2 {
		t.Errorf("wallet = %+v", wallet)
	}
	if rec := requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID, providerA, nil, nil); rec.Code != 403 {
		t.Errorf("provider read wallet = %d, want 403", rec.Code)
	}

	// ledger pagination: limit=1 → page 1 (cursor), page 2 (no cursor)
	page1 := requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID+"/ledger?limit=1", internal, nil, nil)
	if page1.Code != 200 {
		t.Fatalf("ledger page1: %d %s", page1.Code, page1.Body.String())
	}
	var ledger1 struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(page1.Body.Bytes(), &ledger1); err != nil {
		t.Fatal(err)
	}
	if len(ledger1.Entries) != 1 || ledger1.NextCursor == "" {
		t.Fatalf("page1 = %d entries, cursor empty=%v", len(ledger1.Entries), ledger1.NextCursor == "")
	}
	page2 := requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID+"/ledger?limit=1&cursor="+ledger1.NextCursor, internal, nil, nil)
	var ledger2 struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(page2.Body.Bytes(), &ledger2); err != nil {
		t.Fatal(err)
	}
	if len(ledger2.Entries) != 1 || ledger2.NextCursor != "" {
		t.Errorf("page2 = %d entries, cursor = %q", len(ledger2.Entries), ledger2.NextCursor)
	}

	// reconciliation consistent: stored == calculated, difference 0.00
	recon := requestJSON(t, h, http.MethodPost, "/wallets/"+opened.ID+"/reconciliation", internal, nil, nil)
	if recon.Code != 200 {
		t.Fatalf("reconciliation: %d %s", recon.Code, recon.Body.String())
	}
	var result struct {
		StoredBalance     domain.Money `json:"storedBalance"`
		CalculatedBalance domain.Money `json:"calculatedBalance"`
		Difference        domain.Money `json:"difference"`
		Consistent        bool         `json:"consistent"`
		CheckedEntries    int          `json:"checkedEntries"`
	}
	if err := json.Unmarshal(recon.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Consistent || result.Difference.String() != "0.00" || result.CheckedEntries != 2 {
		t.Errorf("reconciliation = %+v", result)
	}
	if result.StoredBalance.String() != "75.00" || result.CalculatedBalance.String() != "75.00" {
		t.Errorf("balances = %s / %s", result.StoredBalance.String(), result.CalculatedBalance.String())
	}

	// divergence reported, not fixed
	if _, err := pool.Exec(t.Context(), `UPDATE wallets SET balance_units = 9000 WHERE id = $1::uuid`, opened.ID); err != nil {
		t.Fatal(err)
	}
	recon = requestJSON(t, h, http.MethodPost, "/wallets/"+opened.ID+"/reconciliation", internal, nil, nil)
	if recon.Code != 200 {
		t.Fatalf("divergent reconciliation: %d", recon.Code)
	}
	if err := json.Unmarshal(recon.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Consistent || result.Difference.String() != "15.00" {
		t.Errorf("divergence = %+v", result)
	}
	// balance untouched
	rec = requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID, internal, nil, nil)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wallet); err != nil {
		t.Fatal(err)
	}
	if wallet.Balance.Amount != "90.00" {
		t.Errorf("reconciliation must not fix the balance: %s", wallet.Balance.Amount)
	}

	// provider cannot reconcile or read ledger
	if rec := requestJSON(t, h, http.MethodPost, "/wallets/"+opened.ID+"/reconciliation", providerA, nil, nil); rec.Code != 403 {
		t.Errorf("provider reconciliation = %d, want 403", rec.Code)
	}
	if rec := requestJSON(t, h, http.MethodGet, "/wallets/"+opened.ID+"/ledger", providerA, nil, nil); rec.Code != 403 {
		t.Errorf("provider ledger = %d, want 403", rec.Code)
	}
}
