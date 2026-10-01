DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS inbox;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS wager_transactions;
DROP TABLE IF EXISTS wallets;
DROP FUNCTION IF EXISTS ledger_entries_append_only();

CREATE TABLE _migrations_smoke (v INT);
INSERT INTO _migrations_smoke VALUES (1);
