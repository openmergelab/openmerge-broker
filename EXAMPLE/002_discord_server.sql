-- 002_discord_server.sql
-- Adds discord_handle to users table for channel naming.
-- Adds server_member flag to track onboarding completion.
-- Both are required for Option 2 channel creation.

-- discord_handle: the human-readable handle e.g. "maya_builds"
-- Used to name the match channel e.g. "meet-maya-james"
-- Never used for matching logic — only for UX

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS discord_handle TEXT,
    ADD COLUMN IF NOT EXISTS server_member  BOOLEAN NOT NULL DEFAULT false;

-- Index for quickly finding non-members who need a server invite
CREATE INDEX IF NOT EXISTS idx_users_server_member
    ON users(server_member)
    WHERE server_member = false;
