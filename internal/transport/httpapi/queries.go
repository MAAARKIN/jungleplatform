package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/auth"
	"github.com/maaarkin/jungleplatform/internal/usecase"
)

// QueriesHandler serves wallet reads, ledger pagination and reconciliation.
// Wallet operations are restricted to the internal service identity.
type QueriesHandler struct {
	wallets   domain.WalletRepo
	ledger    domain.LedgerRepo
	reconcile *usecase.Reconcile
}

// NewQueriesHandler builds the handler.
func NewQueriesHandler(wallets domain.WalletRepo, ledger domain.LedgerRepo, reconcile *usecase.Reconcile) *QueriesHandler {
	return &QueriesHandler{wallets: wallets, ledger: ledger, reconcile: reconcile}
}

// Register mounts the query endpoints.
func (h *QueriesHandler) Register(r chi.Router) {
	r.Get("/wallets/{walletId}", h.getWallet)
	r.Get("/wallets/{walletId}/ledger", h.listLedger)
	r.Post("/wallets/{walletId}/reconciliation", h.reconciliation)
}

func requireInternal(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		unauthorized(w, "missing identity")
		return nil, false
	}
	if claims.ProviderID != "internal" {
		forbidden(w, "wallet operations are restricted to the internal service")
		return nil, false
	}
	return claims, true
}

// getWallet implements GET /wallets/:walletId.
func (h *QueriesHandler) getWallet(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInternal(w, r); !ok {
		return
	}
	wallet, err := h.wallets.GetByID(r.Context(), chi.URLParam(r, "walletId"))
	if err != nil {
		notFoundOrError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       wallet.ID(),
		"playerId": wallet.PlayerID(),
		"balance":  wallet.Balance(),
		"version":  wallet.Version(),
	})
}

// listLedger implements GET /wallets/:walletId/ledger?cursor=&limit=50 with an
// opaque cursor and stable (created_at, id) ordering.
func (h *QueriesHandler) listLedger(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInternal(w, r); !ok {
		return
	}
	walletID := chi.URLParam(r, "walletId")
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			badRequest(w, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	entries, nextCursor, err := h.ledger.ListByWallet(r.Context(), walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		notFoundOrError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]any{
			"id":            e.ID(),
			"transactionId": e.TransactionID(),
			"direction":     e.Direction(),
			"money":         e.Money(),
			"balanceBefore": e.BalanceBefore(),
			"balanceAfter":  e.BalanceAfter(),
			"createdAt":     e.CreatedAt(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"walletId":   walletID,
		"entries":    items,
		"nextCursor": nextCursor,
		"limit":      limit,
	})
}

// reconciliation implements POST /wallets/:walletId/reconciliation. It never
// changes the stored balance.
func (h *QueriesHandler) reconciliation(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireInternal(w, r); !ok {
		return
	}
	res, err := h.reconcile.Execute(r.Context(), usecase.ReconcileInput{
		WalletID: chi.URLParam(r, "walletId"),
	})
	if err != nil {
		notFoundOrError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"walletId":          res.WalletID,
		"storedBalance":     res.StoredBalance,
		"calculatedBalance": res.CalculatedBalance,
		"difference":        res.Difference,
		"consistent":        res.Consistent,
		"checkedEntries":    res.CheckedEntries,
	})
}
