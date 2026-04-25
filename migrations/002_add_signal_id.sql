-- 002_add_signal_id.sql
-- Add signal_id column to agent_signals for broker API lookups.
-- The broker generates a unique sig_XXXXXXXXXXXX identifier per signal.

ALTER TABLE agent_signals
    ADD COLUMN IF NOT EXISTS signal_id VARCHAR(16) UNIQUE;

-- Backfill existing rows (if any) with a placeholder so NOT NULL can be set.
-- In practice, the broker always provides signal_id on upsert.
UPDATE agent_signals SET signal_id = 'sig_' || substr(md5(anonymous_id::text), 1, 12)
WHERE signal_id IS NULL;

ALTER TABLE agent_signals
    ALTER COLUMN signal_id SET NOT NULL;

-- Index for GET /signal/{signalId} lookups (unique already creates one, but explicit for clarity)
-- CREATE UNIQUE INDEX is idempotent with IF NOT EXISTS
CREATE UNIQUE INDEX IF NOT EXISTS idx_signal_id ON agent_signals(signal_id);

-- B-tree index on expires_at for cleanup queries
CREATE INDEX IF NOT EXISTS idx_expires_at ON agent_signals(expires_at);
