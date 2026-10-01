package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/auth"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// WalletsHandler serves the wallet management endpoints. Wallet operations
// are internal-service only: the authenticated identity must be "internal".
type WalletsHandler struct {
	openWallet *usecase.OpenWallet
}

// NewWalletsHandler builds the handler.
func NewWalletsHandler(openWallet *usecase.OpenWallet) *WalletsHandler {
	return &WalletsHandler{openWallet: openWallet}
}

// Routes mounts the wallet endpoints as a standalone router.
func (h *WalletsHandler) Routes() chi.Router {
	r := chi.NewRouter()
	h.Register(r)
	return r
}

// Register mounts the wallet endpoints onto an existing router.
func (h *WalletsHandler) Register(r chi.Router) {
	r.Post("/wallets", h.open)
}

type openWalletRequest struct {
	PlayerID       string       `json:"playerId"`
	InitialBalance domain.Money `json:"initialBalance"`
}

func (h *WalletsHandler) open(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok || claims.ProviderID != "internal" {
		forbidden(w, "wallet operations are restricted to the internal service")
		return
	}

	var req openWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "malformed JSON body")
		return
	}
	if req.PlayerID == "" {
		badRequest(w, "playerId is required")
		return
	}

	out, err := h.openWallet.Execute(r.Context(), usecase.OpenWalletInput{
		PlayerID:       req.PlayerID,
		InitialBalance: req.InitialBalance,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrWalletConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet already exists for this player and currency"})
		case errors.Is(err, domain.ErrInvalidMoney), errors.Is(err, domain.ErrInvalidInput), errors.Is(err, domain.ErrCurrencyMismatch):
			badRequest(w, err.Error())
		default:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transient failure"})
		}
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":       out.WalletID,
		"playerId": out.PlayerID,
		"balance":  out.Balance,
		"version":  out.Version,
	})
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_input", "message": msg})
}

func forbidden(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "message": msg})
}

func unauthorized(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "message": msg})
}
