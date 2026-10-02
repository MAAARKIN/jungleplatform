-- Pending reference retry tracking for the reference-resolution worker.
ALTER TABLE wager_transactions ADD COLUMN reference_attempts INT NOT NULL DEFAULT 0;
ALTER TABLE wager_transactions ADD COLUMN next_reference_attempt_at TIMESTAMPTZ;