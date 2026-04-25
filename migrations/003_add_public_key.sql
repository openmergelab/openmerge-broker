-- 003_add_public_key.sql
-- Add missing public_key column to agent_signals.

ALTER TABLE agent_signals
    ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';
