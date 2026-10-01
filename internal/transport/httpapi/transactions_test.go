//go:build integration

package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

func wagerBody(providerID, extID string, kind string, amount string) map[string]any {
	return map[string]any{
		"providerId":            providerID,
		"externalTransactionId": extID,
		"playerId":              "player-wager-1",
		"walletId":              "@WALLET@",
		"roundId":               "round-1",
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money":                 map[string]any{"amount": amount, "currency": "BRL"},
	}
}

// setupWallet opens a 100.00 wallet for the wager tests.
func setupWallet(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	rec := postJSON(t, h, "/wallets", token, walletRequest{
		PlayerID:       "player-wager-1",
		InitialBalance: mustMoneyDTO(t, "100.00"),
	})
	if rec.Code != 201 {
		t.Fatalf("setup wallet: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.ID
}

func postWager(t *testing.T, h http.Handler, token string, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return requestJSON(t, h, http.MethodPost, "/wagering/transactions", token, map[string]string{"Idempotency-Key": key}, body)
}

func TestProcessBetSynchronousAndReplay(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	walletID := setupWallet(t, h, internal)
	providerA := tokenFor(t, "provider-a", "provider-a-secret")

	body := wagerBody("provider-a", "tx-bet-1", "BET", "25.00")
	body["walletId"] = walletID

	rec := postWager(t, h, providerA, "provider-a:tx-bet-1", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TransactionID    string        `json:"transactionId"`
		Status           domain.Status `json:"status"`
		Balance          domain.Money  `json:"balance"`
		IdempotentReplay bool          `json:"idempotentReplay"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.StatusProcessed || out.IdempotentReplay || out.Balance.Units() != 7500 {
		t.Errorf("out = %+v", out)
	}

	replay := postWager(t, h, providerA, "provider-a:tx-bet-1", body)
	if replay.Code != 200 {
		t.Fatalf("replay status = %d", replay.Code)
	}
	var replayOut struct {
		IdempotentReplay bool         `json:"idempotentReplay"`
		Balance          domain.Money `json:"balance"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayOut); err != nil {
		t.Fatal(err)
	}
	if !replayOut.IdempotentReplay || replayOut.Balance.Units() != 7500 {
		t.Errorf("replay = %+v", replayOut)
	}
}

func TestProcessWithoutIdempotencyKeyRejected(t *testing.T) {
	h, _ := newStack(t)
	providerA := tokenFor(t, "provider-a", "provider-a-secret")

	body := wagerBody("provider-a", "tx-nokey", "BET", "25.00")
	rec := postJSON(t, h, "/wagering/transactions", providerA, body)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestProcessKeyConflictReturns409(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-conflict", "BET", "25.00")
	body["walletId"] = walletID
	if rec := postWager(t, h, providerA, "provider-a:tx-conflict", body); rec.Code != 200 {
		t.Fatalf("first status = %d", rec.Code)
	}

	conflicting := wagerBody("provider-a", "tx-conflict", "BET", "30.00")
	conflicting["walletId"] = walletID
	if rec := postWager(t, h, providerA, "provider-a:tx-conflict", conflicting); rec.Code != 409 {
		t.Errorf("conflict status = %d, want 409", rec.Code)
	}
}

func TestProcessRejectedReturns422WithFailureCode(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-rejected", "BET", "100.01")
	body["walletId"] = walletID
	rec := postWager(t, h, providerA, "provider-a:tx-rejected", body)
	if rec.Code != 422 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status      domain.Status `json:"status"`
		FailureCode string        `json:"failureCode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.StatusRejected || out.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Errorf("out = %+v", out)
	}

	replay := postWager(t, h, providerA, "provider-a:tx-rejected", body)
	if replay.Code != 422 {
		t.Errorf("replay status = %d", replay.Code)
	}
	var replayOut struct {
		IdempotentReplay bool `json:"idempotentReplay"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayOut); err != nil {
		t.Fatal(err)
	}
	if !replayOut.IdempotentReplay {
		t.Error("rejection replay must be idempotent")
	}
}

func TestProcessOpeningKindRejected(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-opening", "OPENING", "25.00")
	body["walletId"] = walletID
	rec := postWager(t, h, internal, "provider-a:tx-opening", body)
	if rec.Code != 400 {
		t.Errorf("OPENING via HTTP status = %d, want 400", rec.Code)
	}
}

func TestProviderIsolationOnProcessAndQueries(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")
	providerB := tokenFor(t, "provider-b", "provider-b-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-iso", "BET", "25.00")
	body["walletId"] = walletID
	rec := postWager(t, h, providerA, "provider-a:tx-iso", body)
	if rec.Code != 200 {
		t.Fatalf("setup bet: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// provider-a impersonating provider-b → 403
	impersonated := wagerBody("provider-b", "tx-iso", "BET", "25.00")
	impersonated["walletId"] = walletID
	if rec := postWager(t, h, providerA, "provider-a:tx-impersonated", impersonated); rec.Code != 403 {
		t.Errorf("impersonation status = %d, want 403", rec.Code)
	}

	// provider-b cannot fetch provider-a's transaction by id → 404
	req := getRequest(t, h, "/wagering/transactions/"+created.TransactionID, providerB)
	if req.Code != 404 {
		t.Errorf("cross-provider GET by id = %d, want 404", req.Code)
	}

	// provider-a can fetch its own by id and by external
	if rec := getRequest(t, h, "/wagering/transactions/"+created.TransactionID, providerA); rec.Code != 200 {
		t.Errorf("own GET by id = %d", rec.Code)
	}
	if rec := getRequest(t, h, "/providers/provider-a/wagering/transactions/tx-iso", providerA); rec.Code != 200 {
		t.Errorf("own GET by external = %d", rec.Code)
	}

	// provider-b cannot fetch through the provider path either
	if rec := getRequest(t, h, "/providers/provider-a/wagering/transactions/tx-iso", providerB); rec.Code != 404 {
		t.Errorf("cross-provider GET by external = %d, want 404", rec.Code)
	}

	// internal sees everything
	if rec := getRequest(t, h, "/wagering/transactions/"+created.TransactionID, internal); rec.Code != 200 {
		t.Errorf("internal GET = %d", rec.Code)
	}

	// unauthenticated → 401
	if rec := getRequest(t, h, "/wagering/transactions/"+created.TransactionID, ""); rec.Code != 401 {
		t.Errorf("unauthenticated GET = %d, want 401", rec.Code)
	}
}

func TestLossViaHTTPKeepsBalance(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-loss", "LOSS", "0.00")
	body["walletId"] = walletID
	rec := postWager(t, h, providerA, "provider-a:tx-loss", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Balance domain.Money `json:"balance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Balance.Units() != 10000 {
		t.Errorf("loss balance = %s, want 100.00", out.Balance.String())
	}
}

func TestRefundWithoutReferencePending202(t *testing.T) {
	h, _ := newStack(t)
	internal := tokenFor(t, "internal", "internal-secret")
	providerA := tokenFor(t, "provider-a", "provider-a-secret")
	walletID := setupWallet(t, h, internal)

	body := wagerBody("provider-a", "tx-refund", "REFUND", "25.00")
	body["walletId"] = walletID
	body["referenceExternalTransactionId"] = "tx-not-yet"

	rec := postWager(t, h, providerA, "provider-a:tx-refund", body)
	if rec.Code != 202 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status domain.Status `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.StatusPendingReference {
		t.Errorf("status = %s, want PENDING_REFERENCE", out.Status)
	}
}

func getRequest(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	return requestJSON(t, h, http.MethodGet, path, token, nil, nil)
}
