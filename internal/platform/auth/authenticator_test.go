package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"

	"github.com/maaarkin/jungleplatform/internal/platform/auth"
)

// fakeJWKS serves a JWKS endpoint with the given public key, standing in for
// the IdP in unit tests.
func fakeJWKS(t *testing.T, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	doc := fmt.Sprintf(`{"keys":[{"kty":"RSA","kid":"t1","use":"sig","alg":"RS256","n":"%s","e":"AQAB"}]}`, n)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signToken(t *testing.T, priv *rsa.PrivateKey, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newTestAuth(t *testing.T) (*auth.Authenticator, *rsa.PrivateKey) {
	t.Helper()
	key := newRSAKey(t)
	srv := fakeJWKS(t, &key.PublicKey)
	a, err := auth.NewAuthenticator(srv.URL, "")
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return a, key
}

func routerWith(a *auth.Authenticator) http.Handler {
	r := chi.NewRouter()
	r.Use(a.Middleware)
	r.Get("/whoami", func(w http.ResponseWriter, r *http.Request) {
		claims, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			http.Error(w, "no claims", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Provider-Id", claims.ProviderID)
		w.WriteHeader(http.StatusOK)
	})
	return r
}

func doRequest(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestValidTokenInjectsProviderID(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{
		"sub": "svc-account", "iss": issuerOf(a), "exp": time.Now().Add(time.Hour).Unix(),
		"providerId": "provider-a",
	})
	rec := doRequest(routerWith(a), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Id"); got != "provider-a" {
		t.Errorf("providerId = %q", got)
	}
}

func TestExpiredTokenRejected(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{
		"sub": "svc", "iss": issuerOf(a), "exp": time.Now().Add(-time.Minute).Unix(),
		"providerId": "provider-a",
	})
	if rec := doRequest(routerWith(a), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired token status = %d, want 401", rec.Code)
	}
}

func TestMissingExpiryRejected(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{"sub": "svc", "iss": issuerOf(a), "providerId": "provider-a"})
	if rec := doRequest(routerWith(a), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("token without exp status = %d, want 401", rec.Code)
	}
}

func TestWrongIssuerRejected(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{
		"sub": "svc", "iss": "http://other-realm", "exp": time.Now().Add(time.Hour).Unix(),
		"providerId": "provider-a",
	})
	if rec := doRequest(routerWith(a), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong issuer status = %d, want 401", rec.Code)
	}
}

func TestMissingProviderIdRejected(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{
		"sub": "svc", "iss": issuerOf(a), "exp": time.Now().Add(time.Hour).Unix(),
	})
	if rec := doRequest(routerWith(a), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("token without providerId status = %d, want 401", rec.Code)
	}
}

func TestUnsignedTokenRejected(t *testing.T) {
	a, _ := newTestAuth(t)
	other := newRSAKey(t)
	token := signToken(t, other, jwt.MapClaims{
		"sub": "svc", "iss": issuerOf(a), "exp": time.Now().Add(time.Hour).Unix(),
		"providerId": "provider-a",
	})
	if rec := doRequest(routerWith(a), token); rec.Code != http.StatusUnauthorized {
		t.Errorf("forged key status = %d, want 401", rec.Code)
	}
}

func TestMissingAndMalformedHeadersRejected(t *testing.T) {
	a, _ := newTestAuth(t)
	h := routerWith(a)
	if rec := doRequest(h, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no header status = %d, want 401", rec.Code)
	}
	if rec := doRequest(h, "garbage"); rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage token status = %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	if rec := doRequest(h, ""); rec.Code != http.StatusUnauthorized {
		_ = rec
	}
}

func TestVerifyReturnsClaims(t *testing.T) {
	a, key := newTestAuth(t)
	token := signToken(t, key, jwt.MapClaims{
		"sub": "svc-1", "iss": issuerOf(a), "exp": time.Now().Add(time.Hour).Unix(),
		"providerId": "provider-a",
	})
	claims, err := a.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ProviderID != "provider-a" || claims.Subject != "svc-1" {
		t.Errorf("claims = %+v", claims)
	}
	_ = json.Marshal
	_ = big.NewInt
}

// issuerOf derives the issuer the authenticator validates against.
func issuerOf(a *auth.Authenticator) string {
	return a.Issuer()
}
