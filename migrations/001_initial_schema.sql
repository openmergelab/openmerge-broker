-- 001_initial_schema.sql
-- Merge broker database schema
-- Three tables. No profile data. No photos. No messages.

-- ─── Users ────────────────────────────────────────────────────────────────────
-- Anonymous user records.
-- Deliberately contains no personally identifying information.
-- discord_id_hash is SHA-256 of the raw Discord ID — not reversible
-- without the original. We need discord_id to create DM channels,
-- but store only the hash here. The skill holds the raw ID locally.

CREATE TABLE IF NOT EXISTS users (
    anonymous_id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    discord_id_hash       TEXT        UNIQUE NOT NULL,
    discord_id            TEXT        NOT NULL,        -- needed to create Discord DM
    age_verified          BOOLEAN     NOT NULL DEFAULT false,
    verified_at           TIMESTAMPTZ,
    verification_provider TEXT,
    push_token            TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen             TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Agent Signals ────────────────────────────────────────────────────────────
-- Active availability signals.
-- This is the maximum information the broker ever holds about a user.
-- preference_vector is encrypted on the device before upload —
-- the broker cannot decrypt it.

CREATE TABLE IF NOT EXISTS agent_signals (
    anonymous_id        UUID        PRIMARY KEY REFERENCES users(anonymous_id) ON DELETE CASCADE,
    location_h3         TEXT        NOT NULL,
    seeking             TEXT        NOT NULL CHECK (seeking IN ('M', 'F', 'NB', 'any')),
    age_min             INT         NOT NULL CHECK (age_min >= 18),
    age_max             INT         NOT NULL CHECK (age_max <= 120),
    preference_vector   BYTEA,                          -- encrypted, broker cannot read
    public_key          TEXT        NOT NULL,
    matched             BOOLEAN     NOT NULL DEFAULT false,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '7 days',
    CONSTRAINT age_range_valid CHECK (age_min <= age_max)
);

CREATE INDEX IF NOT EXISTS idx_signals_location  ON agent_signals(location_h3);
CREATE INDEX IF NOT EXISTS idx_signals_active    ON agent_signals(matched, expires_at)
    WHERE matched = false;

-- ─── Matches ─────────────────────────────────────────────────────────────────
-- Fully anonymised match records.
-- No reason stored. No score stored. No agent conversation stored.
-- discord_channel_id is the only post-match data we hold.

CREATE TABLE IF NOT EXISTS matches (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_a              UUID        NOT NULL REFERENCES users(anonymous_id),
    user_b              UUID        NOT NULL REFERENCES users(anonymous_id),
    discord_channel_id  TEXT        NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_matches_user_a ON matches(user_a);
CREATE INDEX IF NOT EXISTS idx_matches_user_b ON matches(user_b);

-- ─── Stripe Verification Sessions ────────────────────────────────────────────
-- Temporary tracking of in-flight age verification sessions.
-- Deleted once verification resolves.

CREATE TABLE IF NOT EXISTS verification_sessions (
    session_id          TEXT        PRIMARY KEY,
    anonymous_id        UUID        NOT NULL REFERENCES users(anonymous_id) ON DELETE CASCADE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at          TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '10 minutes'
);
