package usecase

import (
	"context"

	"github.com/maaarkin/jungleplatform/internal/domain"
	"github.com/maaarkin/jungleplatform/internal/platform/metrics"
)

// Reconcile compares the stored wallet balance against the balance rebuilt
// from the ledger. It is read-only: a divergence is reported, never fixed.
type Reconcile struct {
	Wallets       domain.WalletRepo
	Reconstructor domain.Reconstructor
	Metrics       *metrics.Registry
}

// NewReconcile builds the use case; metrics may be nil in tests.
func NewReconcile(wallets domain.WalletRepo, reconstructor domain.Reconstructor, m *metrics.Registry) *Reconcile {
	return &Reconcile{Wallets: wallets, Reconstructor: reconstructor, Metrics: m}
}

// ReconcileInput identifies the wallet to check.
type ReconcileInput struct {
	WalletID string
}

// ReconcileOutput reports the comparison result. Difference is
// storedBalance minus calculatedBalance.
type ReconcileOutput struct {
	WalletID          string
	StoredBalance     domain.Money
	CalculatedBalance domain.Money
	Difference        domain.Money
	Consistent        bool
	CheckedEntries    int
}

// Execute runs the comparison over a consistent view of the data.
func (u *Reconcile) Execute(ctx context.Context, in ReconcileInput) (ReconcileOutput, error) {
	w, err := u.Wallets.GetByID(ctx, in.WalletID)
	if err != nil {
		return ReconcileOutput{}, err
	}
	calculated, checked, err := u.Reconstructor.RebuildBalance(ctx, in.WalletID)
	if err != nil {
		return ReconcileOutput{}, err
	}
	difference, err := w.Balance().Sub(calculated)
	if err != nil {
		return ReconcileOutput{}, err
	}
	if u.Metrics != nil && difference.Units() != 0 {
		u.Metrics.ReconciliationDiver.Inc()
	}
	return ReconcileOutput{
		WalletID:          in.WalletID,
		StoredBalance:     w.Balance(),
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    checked,
	}, nil
}
