//go:build integration

package migrate_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/maaarkin/jungleplatform/db/migrations"
	"github.com/maaarkin/jungleplatform/internal/platform/migrate"
)

func schemaDB(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	if err := migrate.Run(dsn(t), migrations.FS, "up"); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	return conn
}

func execErr(t *testing.T, conn *pgx.Conn, sql string, args ...any) error {
	t.Helper()
	ctx := context.Background()
	_, err := conn.Exec(ctx, sql, args...)
	return err
}

func insertWalletSQL(playerID string, balance int64) string {
	return `INSERT INTO wallets (id, player_id, currency, balance_units, version, created_at, updated_at)
		VALUES (gen_random_uuid(), '` + playerID + `', 'BRL', ` + itoa(balance) + `, 1, now(), now())`
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestSchemaRejectsNegativeWalletBalance(t *testing.T) {
	conn := schemaDB(t)
	err := execErr(t, conn, insertWalletSQL("schema-neg", -1))
	if err == nil || !strings.Contains(err.Error(), "wallets_balance_non_negative") {
		t.Errorf("err = %v, want wallets_balance_non_negative", err)
	}
}

func TestSchemaRejectsDuplicatePlayerCurrency(t *testing.T) {
	conn := schemaDB(t)
	if err := execErr(t, conn, insertWalletSQL("schema-dup", 100)); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := execErr(t, conn, insertWalletSQL("schema-dup", 200))
	if err == nil || !strings.Contains(err.Error(), "wallets_player_currency_unique") {
		t.Errorf("err = %v, want wallets_player_currency_unique", err)
	}
}

func TestSchemaLedgerIsAppendOnly(t *testing.T) {
	conn := schemaDB(t)
	ctx := context.Background()

	var walletID string
	if err := conn.QueryRow(ctx, `INSERT INTO wallets (id, player_id, currency, balance_units, version, created_at, updated_at)
		VALUES (gen_random_uuid(), 'schema-ledger', 'BRL', 100000, 1, now(), now()) RETURNING id`).Scan(&walletID); err != nil {
		t.Fatalf("wallet: %v", err)
	}
	var txID string
	if err := conn.QueryRow(ctx, `INSERT INTO wager_transactions (id, source, player_id, wallet_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'INTERNAL', 'schema-ledger', $1, 'OPENING', 100000, 'BRL', 'PROCESSED', now(), now()) RETURNING id`, walletID).Scan(&txID); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	var entryID string
	if err := conn.QueryRow(ctx, `INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, money_units, currency, balance_before_units, balance_after_units, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'CREDIT', 100000, 'BRL', 0, 100000, now()) RETURNING id`, walletID, txID).Scan(&entryID); err != nil {
		t.Fatalf("entry: %v", err)
	}

	err := execErr(t, conn, `UPDATE ledger_entries SET money_units = 1 WHERE id = $1`, entryID)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("update err = %v, want append-only", err)
	}
	err = execErr(t, conn, `DELETE FROM ledger_entries WHERE id = $1`, entryID)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("delete err = %v, want append-only", err)
	}
}

func TestSchemaRejectsDuplicateLedgerPair(t *testing.T) {
	conn := schemaDB(t)
	ctx := context.Background()

	var walletID string
	if err := conn.QueryRow(ctx, `INSERT INTO wallets (id, player_id, currency, balance_units, version, created_at, updated_at)
		VALUES (gen_random_uuid(), 'schema-dupledger', 'BRL', 100000, 1, now(), now()) RETURNING id`).Scan(&walletID); err != nil {
		t.Fatalf("wallet: %v", err)
	}
	var txID string
	if err := conn.QueryRow(ctx, `INSERT INTO wager_transactions (id, source, player_id, wallet_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'INTERNAL', 'schema-dupledger', $1, 'OPENING', 100000, 'BRL', 'PROCESSED', now(), now()) RETURNING id`, walletID).Scan(&txID); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	insert := `INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, money_units, currency, balance_before_units, balance_after_units, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'CREDIT', 100000, 'BRL', 0, 100000, now())`
	if err := execErr(t, conn, insert, walletID, txID); err != nil {
		t.Fatalf("first entry: %v", err)
	}
	err := execErr(t, conn, insert, walletID, txID)
	if err == nil || !strings.Contains(err.Error(), "ledger_wallet_transaction_unique") {
		t.Errorf("err = %v, want ledger_wallet_transaction_unique", err)
	}
}

func TestSchemaRejectsIncoherentSourceKind(t *testing.T) {
	conn := schemaDB(t)
	ctx := context.Background()

	var walletID string
	if err := conn.QueryRow(ctx, `INSERT INTO wallets (id, player_id, currency, balance_units, version, created_at, updated_at)
		VALUES (gen_random_uuid(), 'schema-source', 'BRL', 100000, 1, now(), now()) RETURNING id`).Scan(&walletID); err != nil {
		t.Fatalf("wallet: %v", err)
	}

	externalOpening := `INSERT INTO wager_transactions (id, source, provider_id, external_transaction_id, idempotency_key, payload_hash, player_id, wallet_id, round_id, game_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'EXTERNAL', 'provider-a', 'ext-1', 'k1', 'h1', 'schema-source', $1, 'r', 'g', 'OPENING', 100, 'BRL', 'PENDING', now(), now())`
	if err := execErr(t, conn, externalOpening, walletID); err == nil || !strings.Contains(err.Error(), "wager_transactions_source_kind") {
		t.Errorf("external OPENING err = %v, want wager_transactions_source_kind", err)
	}

	internalBet := `INSERT INTO wager_transactions (id, source, player_id, wallet_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'INTERNAL', 'schema-source', $1, 'BET', 100, 'BRL', 'PENDING', now(), now())`
	if err := execErr(t, conn, internalBet, walletID); err == nil || !strings.Contains(err.Error(), "wager_transactions_source_kind") {
		t.Errorf("internal BET err = %v, want wager_transactions_source_kind", err)
	}

	reversalNoRef := `INSERT INTO wager_transactions (id, source, provider_id, external_transaction_id, idempotency_key, payload_hash, player_id, wallet_id, round_id, game_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'EXTERNAL', 'provider-a', 'ext-2', 'k2', 'h2', 'schema-source', $1, 'r', 'g', 'REFUND', 100, 'BRL', 'PENDING', now(), now())`
	if err := execErr(t, conn, reversalNoRef, walletID); err == nil || !strings.Contains(err.Error(), "wager_transactions_reversal_reference") {
		t.Errorf("REFUND without reference err = %v, want wager_transactions_reversal_reference", err)
	}

	duplicate := `INSERT INTO wager_transactions (id, source, provider_id, external_transaction_id, idempotency_key, payload_hash, player_id, wallet_id, round_id, game_id, kind, money_units, currency, status, created_at, updated_at)
		VALUES (gen_random_uuid(), 'EXTERNAL', 'provider-a', 'ext-dup', 'k4', 'h4', 'schema-source', $1, 'r', 'g', 'BET', 100, 'BRL', 'PENDING', now(), now())`
	if err := execErr(t, conn, duplicate, walletID); err != nil {
		t.Fatalf("first external bet: %v", err)
	}
	duplicate2 := strings.Replace(duplicate, "'k4'", "'k5'", 1)
	err := execErr(t, conn, duplicate2, walletID)
	if err == nil || !strings.Contains(err.Error(), "wager_transactions_provider_external_unique") {
		t.Errorf("duplicate (provider, external) err = %v, want unique violation", err)
	}
}

func TestSchemaOutboxAndInboxAcceptRows(t *testing.T) {
	conn := schemaDB(t)

	if err := execErr(t, conn, `INSERT INTO outbox (event_id, aggregate_id, event_type, payload, occurred_at)
		VALUES (gen_random_uuid(), 'agg', 'WalletBalanceChanged', '{"k":"v"}', now())`); err != nil {
		t.Errorf("outbox insert: %v", err)
	}
	if err := execErr(t, conn, `INSERT INTO inbox (id, consumer_name, message_id, payload_hash, received_at)
		VALUES (gen_random_uuid(), 'worker-1', 'msg-1', 'h', now())`); err != nil {
		t.Errorf("inbox insert: %v", err)
	}
	err := execErr(t, conn, `INSERT INTO inbox (id, consumer_name, message_id, payload_hash, received_at)
		VALUES (gen_random_uuid(), 'worker-1', 'msg-1', 'h', now())`)
	if err == nil || !strings.Contains(err.Error(), "inbox_consumer_message_unique") {
		t.Errorf("duplicate inbox err = %v, want unique violation", err)
	}
}
