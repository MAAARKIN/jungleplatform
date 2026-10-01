package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

// Reconstructor implements domain.Reconstructor: it rebuilds a wallet balance
// from the ledger for reconciliation. It is read-only.
type Reconstructor struct {
	pool *pgxpool.Pool
}

// NewReconstructor builds the reconstructor over the given pool.
func NewReconstructor(pool *pgxpool.Pool) *Reconstructor {
	return &Reconstructor{pool: pool}
}

// RebuildBalance sums credits minus debits over all ledger entries of the
// wallet, in (created_at, id) order, and returns the balance with the entry
// count. Within a transaction (WithinTx) it reads a consistent snapshot.
func (r *Reconstructor) RebuildBalance(ctx context.Context, walletID string) (domain.Money, int, error) {
	q := QuerierFor(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT direction, money_units, currency FROM ledger_entries WHERE wallet_id = $1::uuid ORDER BY created_at, id`,
		walletID,
	)
	if err != nil {
		return domain.Money{}, 0, fmt.Errorf("postgres: rebuild scan: %w", err)
	}
	defer rows.Close()

	var currency domain.Currency
	var units int64
	count := 0
	for rows.Next() {
		var dir string
		var entryUnits int64
		var entryCurrency string
		if err := rows.Scan(&dir, &entryUnits, &entryCurrency); err != nil {
			return domain.Money{}, 0, fmt.Errorf("postgres: rebuild row: %w", err)
		}
		if currency == "" {
			currency = domain.Currency(entryCurrency)
		}
		if entryCurrency != string(currency) {
			return domain.Money{}, 0, domain.ErrCurrencyMismatch
		}
		if domain.Direction(dir) == domain.CreditDirection {
			units += entryUnits
		} else {
			units -= entryUnits
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return domain.Money{}, 0, fmt.Errorf("postgres: rebuild rows: %w", err)
	}
	if currency == "" {
		return domain.Money{}, 0, domain.ErrNotFound
	}
	return domain.NewMoneyUnchecked(units, currency), count, nil
}
