-- Financial domain schema. All financial invariants are enforced here, not in
-- application code: uniqueness, non-negative balance and ledger immutability.

DROP TABLE IF EXISTS _migrations_smoke;

CREATE TABLE wallets (
    id            UUID PRIMARY KEY,
    player_id     TEXT NOT NULL,
    currency      TEXT NOT NULL,
    balance_units BIGINT NOT NULL,
    version       BIGINT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_balance_non_negative CHECK (balance_units >= 0),
    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id                               UUID PRIMARY KEY,
    source                           TEXT NOT NULL CHECK (source IN ('INTERNAL', 'EXTERNAL')),
    provider_id                      TEXT,
    external_transaction_id          TEXT,
    idempotency_key                  TEXT,
    payload_hash                     TEXT,
    player_id                        TEXT NOT NULL,
    wallet_id                        UUID NOT NULL REFERENCES wallets (id),
    round_id                         TEXT,
    game_id                          TEXT,
    kind                             TEXT NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    money_units                      BIGINT NOT NULL,
    currency                         TEXT NOT NULL,
    reference_external_transaction_id TEXT,
    resolved_reference_transaction_id UUID,
    status                           TEXT NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    failure_code                     TEXT,
    result_balance_units             BIGINT,
    created_at                       TIMESTAMPTZ NOT NULL,
    updated_at                       TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_transactions_source_kind CHECK (
        (source = 'INTERNAL' AND kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL)
        OR (source = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND idempotency_key IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_reversal_reference CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL
    )
);

CREATE UNIQUE INDEX wager_transactions_idempotency_key_unique
    ON wager_transactions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_provider_external_unique
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE external_transaction_id IS NOT NULL;

CREATE TABLE ledger_entries (
    id                  UUID PRIMARY KEY,
    wallet_id           UUID NOT NULL REFERENCES wallets (id),
    transaction_id      UUID NOT NULL REFERENCES wager_transactions (id),
    direction           TEXT NOT NULL CHECK (direction IN ('CREDIT', 'DEBIT')),
    money_units         BIGINT NOT NULL,
    currency            TEXT NOT NULL,
    balance_before_units BIGINT NOT NULL,
    balance_after_units BIGINT NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_wallet_transaction_unique UNIQUE (wallet_id, transaction_id),
    CONSTRAINT ledger_money_positive CHECK (money_units > 0),
    CONSTRAINT ledger_debit_consistent CHECK (
        direction <> 'DEBIT' OR balance_after_units = balance_before_units - money_units
    ),
    CONSTRAINT ledger_credit_consistent CHECK (
        direction <> 'CREDIT' OR balance_after_units = balance_before_units + money_units
    )
);

-- Append-only: any UPDATE or DELETE on the ledger must fail.
CREATE OR REPLACE FUNCTION ledger_entries_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger_entries is append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_entries_no_update
    BEFORE UPDATE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_append_only();

CREATE TRIGGER ledger_entries_no_delete
    BEFORE DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_append_only();

CREATE TABLE inbox (
    id           UUID PRIMARY KEY,
    consumer_name TEXT NOT NULL,
    message_id   TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    CONSTRAINT inbox_consumer_message_unique UNIQUE (consumer_name, message_id)
);

CREATE TABLE outbox (
    event_id        UUID PRIMARY KEY,
    aggregate_id    TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INT NOT NULL DEFAULT 0,
    claimed_by      TEXT,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ
);

CREATE INDEX outbox_pending_idx ON outbox (next_attempt_at) WHERE published_at IS NULL;
