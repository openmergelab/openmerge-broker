-- 008_discord_to_telegram.sql
-- Migrate from Discord to Telegram as the messaging/auth platform.
-- Renames columns and adds new ones for Telegram identity.

-- 1. Rename Discord columns on users table to Telegram equivalents
ALTER TABLE users RENAME COLUMN discord_id_hash TO telegram_id_hash;
ALTER TABLE users RENAME COLUMN discord_id TO telegram_id;
ALTER TABLE users RENAME COLUMN discord_handle TO telegram_handle;

-- 2. Drop server_member (not needed for Telegram DMs)
ALTER TABLE users DROP COLUMN IF EXISTS server_member;

-- 3. Drop the old discord-related index and recreate for telegram
DROP INDEX IF EXISTS idx_users_server_member;

-- 4. Add introduced flag to matches (replaces discord_channel_id for tracking)
ALTER TABLE matches ADD COLUMN IF NOT EXISTS introduced BOOLEAN NOT NULL DEFAULT false;

-- 5. Rename discord_channel_id to intro_channel_id (kept for potential future use)
ALTER TABLE matches RENAME COLUMN discord_channel_id TO intro_channel_id;
