//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/platform/auth"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
	"github.com/maaarkin/jungleplatform/internal/repository/postgres"
	"github.com/maaarkin/jungleplatform/internal/transport/httpapi"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

const defaultPostgresDSN = "postgres://jungle:jungle@localhost:5432/jungle_test?sslmode=disable"
const defaultIssuer = "http://localhost:8180/realms/jungle"

func testDSN(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("TEST_POSTGRES_DSN"); v != "" {
		return v
	}
	return defaultPostgresDSN
}

func issuer(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("KEYCLOAK_ISSUER_URL"); v != "" {
		return v
	}
	return defaultIssuer
}

// newStack wires the real dependencies (postgres, keycloak) for HTTP tests.
func newStack(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if err := migrate.Run(testDSN(t), migrations.FS, "up"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.NewPool(ctx, testDSN(t))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `TRUNCATE ledger_entries, wager_transactions, wallets, inbox, outbox CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	wallets := postgres.NewWalletRepo(pool)
	transactions := postgres.NewTransactionRepo(pool)
	ledger := postgres.NewLedgerRepo(pool)
	outbox := postgres.NewOutboxRepo(pool)
	txm := postgres.NewTxManager(pool)

	openWallet := usecase.NewOpenWallet(txm, wallets, transactions, ledger, outbox)
	processWager := usecase.NewProcessWager(txm, wallets, transactions, ledger, outbox)

	authenticator, err := auth.NewAuthenticator(issuer(t), "")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}

	walletsHandler := httpapi.NewWalletsHandler(openWallet)
	transactionsHandler := httpapi.NewTransactionsHandler(processWager, transactions)
	health := httpapi.NewHealthHandler(func(context.Context) error { return nil })
	h := httpapi.NewRouter(health, authenticator.Middleware, walletsHandler, transactionsHandler)
	return h, pool
}

// tokenFor fetches a client_credentials token from the realm.
func tokenFor(t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	resp, err := http.PostForm(issuer(t)+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.AccessToken == "" {
		t.Fatal("empty token")
	}
	return body.AccessToken
}

func postJSON(t *testing.T, h http.Handler, path string, token string, body any) *httptest.ResponseRecorder {
	return requestJSON(t, h, http.MethodPost, path, token, nil, body)
}

// requestJSON performs an arbitrary method request with optional extra headers.
func requestJSON(t *testing.T, h http.Handler, method, path, token string, headers map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
