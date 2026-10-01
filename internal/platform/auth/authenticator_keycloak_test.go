//go:build integration

package auth_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"

	"github.com/maaarkin/jungleplatform/internal/platform/auth"
)

const defaultIssuer = "http://localhost:8180/realms/jungle"

func issuer(t *testing.T) string {
	t.Helper()
	if v := os.Getenv("KEYCLOAK_ISSUER_URL"); v != "" {
		return v
	}
	return defaultIssuer
}

// fetchToken performs a client_credentials grant against the realm.
func fetchToken(t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	resp, err := http.PostForm(issuer(t)+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if body.AccessToken == "" {
		t.Fatalf("no access token: %s", body.Error)
	}
	return body.AccessToken
}

func TestKeycloakIssuesTokenAndMiddlewareAcceptsIt(t *testing.T) {
	token := fetchToken(t, "provider-a", "provider-a-secret")
	a, err := auth.NewAuthenticator(issuer(t), "")
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	claims, err := a.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ProviderID != "provider-a" {
		t.Errorf("providerId = %q, want provider-a", claims.ProviderID)
	}
}

func TestKeycloakInternalIdentity(t *testing.T) {
	token := fetchToken(t, "internal", "internal-secret")
	a, err := auth.NewAuthenticator(issuer(t), "")
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	claims, err := a.Verify(t.Context(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ProviderID != "internal" {
		t.Errorf("providerId = %q, want internal", claims.ProviderID)
	}
}

func TestKeycloakWrongCredentialsRejected(t *testing.T) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"provider-a"},
		"client_secret": {"wrong-secret"},
	}
	resp, err := http.PostForm(issuer(t)+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
