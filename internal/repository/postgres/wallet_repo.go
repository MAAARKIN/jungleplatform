package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"errors"
	"time"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

const walletColumns = `id, player_id, currency, balance_units, version, created_at, updated_at`

// WalletRepo implements domain.WalletRepo with explicit SQL.
type WalletRepo struct {
	pool *pgxpool.Pool
}

// NewWalletRepo builds the repository over the given pool.
func NewWalletRepo(pool *pgxpool.Pool) *WalletRepo {
	return &WalletRepo{pool: pool}
}

// Insert persists a new wallet. A duplicate (player_id, currency) maps to
// domain.ErrWalletConflict.
func (r *WalletRepo) Insert(ctx context.Context, w *domain.Wallet) error {
	q := QuerierFor(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), string(w.Currency()), w.Balance().Units(),
		w.Version(), w.CreatedAt(), w.UpdatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err, "wallets_player_currency_unique") {
			return domain.ErrWalletConflict
		}
		return fmt.Errorf("postgres: insert wallet: %w", err)
	}
	return nil
}

// GetByID loads a wallet without locking.
func (r *WalletRepo) GetByID(ctx context.Context, id string) (*domain.Wallet, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanWallet(q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id))
}

// GetForUpdate loads a wallet with SELECT ... FOR UPDATE, serializing
// concurrent writers on the same wallet row until the transaction ends.
func (r *WalletRepo) GetForUpdate(ctx context.Context, id string) (*domain.Wallet, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanWallet(q.QueryRow(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id))
}

// GetByPlayerAndCurrency loads the unique wallet of a (player, currency) pair.
func (r *WalletRepo) GetByPlayerAndCurrency(ctx context.Context, playerID string, c domain.Currency) (*domain.Wallet, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanWallet(q.QueryRow(ctx,
		`SELECT `+walletColumns+` FROM wallets WHERE player_id = $1 AND currency = $2`,
		playerID, string(c),
	))
}

// Update persists balance, version and updated_at of the aggregate.
func (r *WalletRepo) Update(ctx context.Context, w *domain.Wallet) error {
	q := QuerierFor(ctx, r.pool)
	tag, err := q.Exec(ctx,
		`UPDATE wallets SET balance_units = $2, version = $3, updated_at = $4 WHERE id = $1`,
		w.ID(), w.Balance().Units(), w.Version(), w.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("postgres: update wallet: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *WalletRepo) scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var id, playerID, currency string
	var units, version int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &playerID, &currency, &units, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: scan wallet: %w", err)
	}
	return domain.RehydrateWallet(id, playerID, domain.NewMoneyUnchecked(units, domain.Currency(currency)), version, createdAt, updatedAt)
}
