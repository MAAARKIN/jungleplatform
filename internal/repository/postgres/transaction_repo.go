package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maaarkin/jungleplatform/internal/domain"
)

const txColumns = `id, source,
	COALESCE(provider_id, ''), COALESCE(external_transaction_id, ''),
	COALESCE(idempotency_key, ''), COALESCE(payload_hash, ''),
	player_id, wallet_id::text, COALESCE(round_id, ''), COALESCE(game_id, ''),
	kind, money_units, currency,
	COALESCE(reference_external_transaction_id, ''), resolved_reference_transaction_id,
	status, COALESCE(failure_code, ''), result_balance_units, created_at, updated_at`

// TransactionRepo implements domain.TransactionRepo with explicit SQL.
type TransactionRepo struct {
	pool *pgxpool.Pool
}

// NewTransactionRepo builds the repository over the given pool.
func NewTransactionRepo(pool *pgxpool.Pool) *TransactionRepo {
	return &TransactionRepo{pool: pool}
}

// Insert persists a new wager transaction.
func (r *TransactionRepo) Insert(ctx context.Context, t *domain.WagerTransaction) error {
	q := QuerierFor(ctx, r.pool)
	var resultUnits *int64
	if t.HasResultBalance() {
		u := t.ResultBalance().Units()
		resultUnits = &u
	}
	var resolvedRef *string
	if t.ResolvedReferenceTransactionID() != "" {
		ref := t.ResolvedReferenceTransactionID()
		resolvedRef = &ref
	}
	_, err := q.Exec(ctx,
		`INSERT INTO wager_transactions (
			id, source, provider_id, external_transaction_id, idempotency_key, payload_hash,
			player_id, wallet_id, round_id, game_id, kind, money_units, currency,
			reference_external_transaction_id, resolved_reference_transaction_id,
			status, failure_code, result_balance_units, created_at, updated_at
		) VALUES (
			$1, $2, nullIf($3, ''), nullIf($4, ''), nullIf($5, ''), nullIf($6, ''),
			$7, $8::uuid, nullIf($9, ''), nullIf($10, ''), $11, $12, $13,
			nullIf($14, ''), $15,
			$16, nullIf($17, ''), $18, $19, $20
		)`,
		t.ID(), string(t.Source()), t.ProviderID(), t.ExternalTransactionID(), t.IdempotencyKey(), t.PayloadHash(),
		t.PlayerID(), t.WalletID(), t.RoundID(), t.GameID(), string(t.Kind()), t.Money().Units(), string(t.Money().Currency()),
		t.ReferenceExternalTransactionID(), resolvedRef,
		string(t.Status()), t.FailureCode(), resultUnits, t.CreatedAt(), t.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("postgres: insert transaction: %w", err)
	}
	return nil
}

// Update persists the mutable transition state: status, failure code, result
// balance, resolved reference and updated_at. Business identity never changes.
func (r *TransactionRepo) Update(ctx context.Context, t *domain.WagerTransaction) error {
	q := QuerierFor(ctx, r.pool)
	var resultUnits *int64
	if t.HasResultBalance() {
		u := t.ResultBalance().Units()
		resultUnits = &u
	}
	var resolvedRef *string
	if t.ResolvedReferenceTransactionID() != "" {
		ref := t.ResolvedReferenceTransactionID()
		resolvedRef = &ref
	}
	tag, err := q.Exec(ctx,
		`UPDATE wager_transactions SET
			status = $2, failure_code = nullIf($3, ''), result_balance_units = $4,
			resolved_reference_transaction_id = $5, updated_at = $6
		WHERE id = $1::uuid`,
		t.ID(), string(t.Status()), t.FailureCode(), resultUnits, resolvedRef, t.UpdatedAt(),
	)
	if err != nil {
		return fmt.Errorf("postgres: update transaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// GetByID loads a transaction by internal id.
func (r *TransactionRepo) GetByID(ctx context.Context, id string) (*domain.WagerTransaction, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanTransaction(q.QueryRow(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE id = $1::uuid`, id))
}

// GetByIdempotencyKey loads a transaction by its idempotency key.
func (r *TransactionRepo) GetByIdempotencyKey(ctx context.Context, key string) (*domain.WagerTransaction, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanTransaction(q.QueryRow(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE idempotency_key = $1`, key))
}

// GetByProviderExternal loads a transaction by (providerId, externalTransactionId).
func (r *TransactionRepo) GetByProviderExternal(ctx context.Context, providerID, externalTransactionID string) (*domain.WagerTransaction, error) {
	q := QuerierFor(ctx, r.pool)
	return r.scanTransaction(q.QueryRow(ctx,
		`SELECT `+txColumns+` FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2`,
		providerID, externalTransactionID,
	))
}

// ListPendingReference returns PENDING_REFERENCE transactions ordered by the
// oldest update, feeding the reference-resolution worker with backoff.
func (r *TransactionRepo) ListPendingReference(ctx context.Context, limit int) ([]*domain.WagerTransaction, error) {
	q := QuerierFor(ctx, r.pool)
	rows, err := q.Query(ctx, `SELECT `+txColumns+` FROM wager_transactions WHERE status = 'PENDING_REFERENCE' ORDER BY updated_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending reference: %w", err)
	}
	defer rows.Close()

	var out []*domain.WagerTransaction
	for rows.Next() {
		tx, err := scanTransactionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (r *TransactionRepo) scanTransaction(row pgx.Row) (*domain.WagerTransaction, error) {
	return scanTransactionRow(row)
}

func scanTransactionRow(row pgx.Row) (*domain.WagerTransaction, error) {
	var (
		id, source, providerID, externalID, idempotencyKey, payloadHash string
		playerID, walletID, roundID, gameID, kind, currency             string
		referenceExternalID, status, failureCode                        string
		resolvedRef                                                     *string
		moneyUnits                                                      int64
		resultUnits                                                     *int64
		createdAt, updatedAt                                            time.Time
	)
	if err := row.Scan(&id, &source, &providerID, &externalID, &idempotencyKey, &payloadHash,
		&playerID, &walletID, &roundID, &gameID,
		&kind, &moneyUnits, &currency,
		&referenceExternalID, &resolvedRef,
		&status, &failureCode, &resultUnits, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: scan transaction: %w", err)
	}

	resultBalance := domain.Money{}
	if resultUnits != nil {
		resultBalance = domain.NewMoneyUnchecked(*resultUnits, domain.Currency(currency))
	}
	return domain.RehydrateTransaction(domain.TransactionState{
		ID:                             id,
		Source:                         domain.Source(source),
		ProviderID:                     providerID,
		ExternalTransactionID:          externalID,
		IdempotencyKey:                 idempotencyKey,
		PayloadHash:                    payloadHash,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        roundID,
		GameID:                         gameID,
		Kind:                           domain.Kind(kind),
		Money:                          domain.NewMoneyUnchecked(moneyUnits, domain.Currency(currency)),
		ReferenceExternalTransactionID: referenceExternalID,
		ResolvedReferenceTransactionID: derefString(resolvedRef),
		Status:                         domain.Status(status),
		FailureCode:                    failureCode,
		ResultBalance:                  resultBalance,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
