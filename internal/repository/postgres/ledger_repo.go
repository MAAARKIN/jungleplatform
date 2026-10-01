package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

const ledgerColumns = `id, wallet_id::text, transaction_id::text, direction, money_units, currency,
	balance_before_units, balance_after_units, created_at`

// LedgerRepo implements domain.LedgerRepo with explicit SQL. There is no
// update or delete: the database enforces append-only.
type LedgerRepo struct {
	pool *pgxpool.Pool
}

// NewLedgerRepo builds the repository over the given pool.
func NewLedgerRepo(pool *pgxpool.Pool) *LedgerRepo {
	return &LedgerRepo{pool: pool}
}

// Insert appends one validated entry. A duplicate (wallet_id, transaction_id)
// maps to domain.ErrDuplicateLedgerEntry.
func (r *LedgerRepo) Insert(ctx context.Context, e domain.LedgerEntry) error {
	q := QuerierFor(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, money_units, currency, balance_before_units, balance_after_units, created_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()),
		e.Money().Units(), string(e.Money().Currency()),
		e.BalanceBefore().Units(), e.BalanceAfter().Units(), e.CreatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err, "ledger_wallet_transaction_unique") {
			return domain.ErrDuplicateLedgerEntry
		}
		return fmt.Errorf("postgres: insert ledger entry: %w", err)
	}
	return nil
}

// ListByWallet pages entries in stable (created_at, id) order. The cursor is
// the base64-encoded id of the last returned entry; empty string starts over.
func (r *LedgerRepo) ListByWallet(ctx context.Context, walletID string, cursor string, limit int) ([]domain.LedgerEntry, string, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	lastID := ""
	if cursor != "" {
		raw, err := base64.URLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: malformed ledger cursor", domain.ErrInvalidInput)
		}
		lastID = string(raw)
	}

	q := QuerierFor(ctx, r.pool)
	var rows pgx.Rows
	var err error
	if lastID == "" {
		rows, err = q.Query(ctx,
			`SELECT `+ledgerColumns+` FROM ledger_entries
			WHERE wallet_id = $1::uuid
			ORDER BY created_at, id
			LIMIT $2`,
			walletID, limit+1,
		)
	} else {
		rows, err = q.Query(ctx,
			`SELECT `+ledgerColumns+` FROM ledger_entries
			WHERE wallet_id = $1::uuid AND (created_at, id) > (SELECT created_at, id FROM ledger_entries WHERE id = $2::uuid)
			ORDER BY created_at, id
			LIMIT $3`,
			walletID, lastID, limit+1,
		)
	}
	if err != nil {
		return nil, "", fmt.Errorf("postgres: list ledger: %w", err)
	}
	defer rows.Close()

	var out []domain.LedgerEntry
	for rows.Next() {
		e, err := scanLedgerRow(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("postgres: list ledger rows: %w", err)
	}
	// the query fetched limit+1: a full page means a next page exists
	if len(out) > limit {
		out = out[:limit]
		return out, base64.URLEncoding.EncodeToString([]byte(out[len(out)-1].ID())), nil
	}
	return out, "", nil
}

func scanLedgerRow(row pgx.Row) (domain.LedgerEntry, error) {
	var id, walletID, transactionID, direction, currency string
	var moneyUnits, beforeUnits, afterUnits int64
	var createdAt time.Time
	if err := row.Scan(&id, &walletID, &transactionID, &direction, &moneyUnits, &currency, &beforeUnits, &afterUnits, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.LedgerEntry{}, domain.ErrNotFound
		}
		return domain.LedgerEntry{}, fmt.Errorf("postgres: scan ledger entry: %w", err)
	}
	return domain.RehydrateLedgerEntry(
		id, walletID, transactionID,
		domain.Direction(direction),
		domain.NewMoneyUnchecked(moneyUnits, domain.Currency(currency)),
		domain.NewMoneyUnchecked(beforeUnits, domain.Currency(currency)),
		domain.NewMoneyUnchecked(afterUnits, domain.Currency(currency)),
		createdAt,
	)
}
