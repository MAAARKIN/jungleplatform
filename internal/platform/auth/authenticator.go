// Package auth validates OAuth 2.0/OIDC access tokens against the external
// IdP (Keycloak) via its JWKS endpoint and carries the authenticated identity
// through the request context.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

type ctxKey struct{}

// Claims is the authenticated identity extracted from the access token.
type Claims struct {
	Subject    string
	ProviderID string
}

// Authenticator verifies tokens against the IdP JWKS with background refresh.
type Authenticator struct {
	jwks   keyfunc.Keyfunc
	issuer string
}

// NewAuthenticator builds an authenticator for the given OIDC issuer URL.
// jwksURL may be empty to derive from the issuer (same host); a separate URL
// is needed when the issuer hostname is not reachable from this process.
func NewAuthenticator(issuerURL, jwksURL string) (*Authenticator, error) {
	issuer := strings.TrimSuffix(issuerURL, "/")
	if jwksURL == "" {
		jwksURL = issuer + "/protocol/openid-connect/certs"
	}
	k, err := keyfunc.NewDefault([]string{jwksURL})
	if err != nil {
		return nil, fmt.Errorf("auth: jwks: %w", err)
	}
	return &Authenticator{jwks: k, issuer: issuer}, nil
}

// Issuer returns the issuer URL this authenticator validates against.
func (a *Authenticator) Issuer() string { return a.issuer }

// Verify parses and validates an access token: RS256 signature from the IdP
// JWKS, matching issuer, non-expired, and the providerId claim present.
func (a *Authenticator) Verify(ctx context.Context, raw string) (*Claims, error) {
	token, err := jwt.Parse(raw, a.jwks.KeyfuncCtx(ctx),
		jwt.WithIssuer(a.issuer),
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid token: %w", err)
	}
	mc, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("auth: unexpected claims type")
	}
	providerID, _ := mc["providerId"].(string)
	if providerID == "" {
		return nil, fmt.Errorf("auth: token lacks providerId claim")
	}
	subject, _ := mc["sub"].(string)
	return &Claims{Subject: subject, ProviderID: providerID}, nil
}

// Middleware rejects requests without a valid bearer token and stores the
// claims in the request context.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			unauthorized(w, "missing bearer token")
			return
		}
		claims, err := a.Verify(r.Context(), raw)
		if err != nil {
			unauthorized(w, "invalid token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, claims)))
	})
}

func bearerToken(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":"unauthorized","message":%q}`, msg)
}

// ClaimsFromContext returns the authenticated claims stored by Middleware.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(ctxKey{}).(*Claims)
	return claims, ok
}
