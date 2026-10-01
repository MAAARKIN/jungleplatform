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

// TransactionsHandler serves the wagering operation endpoints.
type TransactionsHandler struct {
	processWager *usecase.ProcessWager
	transactions domain.TransactionRepo
}

// NewTransactionsHandler builds the handler.
func NewTransactionsHandler(processWager *usecase.ProcessWager, transactions domain.TransactionRepo) *TransactionsHandler {
	return &TransactionsHandler{processWager: processWager, transactions: transactions}
}

// Register mounts the transaction endpoints.
func (h *TransactionsHandler) Register(r chi.Router) {
	r.Post("/wagering/transactions", h.process)
	r.Get("/wagering/transactions/{transactionId}", h.getByID)
	r.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", h.getByProviderExternal)
}

type wagerRequest struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           string       `json:"kind"`
	Money                          domain.Money `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId"`
}

// process implements POST /wagering/transactions.
// Statuses are distinguishable by status code: 200 processed/replay,
// 202 pending reference, 409 idempotency conflict, 400 invalid input,
// 404 wallet not found, 422 business rejection, 503 transient failure.
func (h *TransactionsHandler) process(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		unauthorized(w, "missing identity")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		badRequest(w, "Idempotency-Key header is required")
		return
	}

	var req wagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "malformed JSON body")
		return
	}

	// the authenticated identity determines the authorized providerId
	providerID := claims.ProviderID
	if claims.ProviderID != "internal" {
		if req.ProviderID != "" && req.ProviderID != claims.ProviderID {
			forbidden(w, "authenticated identity does not own this providerId")
			return
		}
		providerID = claims.ProviderID
	}

	out, err := h.processWager.Execute(r.Context(), usecase.ProcessWagerInput{
		ProviderID:                     providerID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       req.PlayerID,
		WalletID:                       req.WalletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           domain.Kind(req.Kind),
		Money:                          req.Money,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrIdempotencyConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "idempotency_conflict"})
		case errors.Is(err, domain.ErrInvalidMoney), errors.Is(err, domain.ErrInvalidInput),
			errors.Is(err, domain.ErrCurrencyMismatch), errors.Is(err, domain.ErrMissingReference):
			badRequest(w, err.Error())
		case errors.Is(err, domain.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		default:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transient_failure"})
		}
		return
	}

	status := http.StatusOK
	if out.Status == domain.StatusPendingReference {
		status = http.StatusAccepted
	}
	if out.Status == domain.StatusRejected {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, map[string]any{
		"transactionId":    out.TransactionID,
		"status":           out.Status,
		"balance":          out.Balance,
		"idempotentReplay": out.Replay,
		"failureCode":      out.FailureCode,
	})
}

// getByID implements GET /wagering/transactions/:transactionId with provider
// isolation: a provider sees only its own transactions.
func (h *TransactionsHandler) getByID(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		unauthorized(w, "missing identity")
		return
	}
	tx, err := h.transactions.GetByID(r.Context(), chi.URLParam(r, "transactionId"))
	if err != nil {
		notFoundOrError(w, err)
		return
	}
	if claims.ProviderID != "internal" && tx.ProviderID() != claims.ProviderID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeTransaction(w, tx)
}

// getByProviderExternal implements GET /providers/:providerId/wagering/
// transactions/:externalTransactionId with provider isolation.
func (h *TransactionsHandler) getByProviderExternal(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		unauthorized(w, "missing identity")
		return
	}
	providerID := chi.URLParam(r, "providerId")
	if claims.ProviderID != "internal" && claims.ProviderID != providerID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	tx, err := h.transactions.GetByProviderExternal(r.Context(), providerID, chi.URLParam(r, "externalTransactionId"))
	if err != nil {
		notFoundOrError(w, err)
		return
	}
	writeTransaction(w, tx)
}

func writeTransaction(w http.ResponseWriter, tx *domain.WagerTransaction) {
	writeJSON(w, http.StatusOK, map[string]any{
		"transactionId":         tx.ID(),
		"providerId":            tx.ProviderID(),
		"externalTransactionId": tx.ExternalTransactionID(),
		"status":                tx.Status(),
		"kind":                  tx.Kind(),
		"money":                 tx.Money(),
		"failureCode":           tx.FailureCode(),
		"createdAt":             tx.CreatedAt(),
	})
}

func notFoundOrError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "transient_failure"})
}
