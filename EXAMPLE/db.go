package db

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a pgx connection pool.
// All broker queries go through here.
type DB struct {
	pool *pgxpool.Pool
}

// New creates a new DB from the DATABASE_URL environment variable.
func New(ctx context.Context) (*DB, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL not set")
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}

	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to db: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return &DB{pool: pool}, nil
}

func (d *DB) Close() {
	d.pool.Close()
}

// ─── User queries ─────────────────────────────────────────────────────────────

type User struct {
	AnonymousID          string
	DiscordIDHash        string
	DiscordID            string
	DiscordHandle        string
	AgeVerified          bool
	VerifiedAt           *time.Time
	VerificationProvider *string
	PushToken            *string
	ServerMember         bool
	CreatedAt            time.Time
	LastSeen             time.Time
}

func (d *DB) UpsertUser(ctx context.Context,
	discordIDHash, discordID, discordHandle string,
) (*User, error) {
	row := d.pool.QueryRow(ctx, `
		INSERT INTO users (discord_id_hash, discord_id, discord_handle)
		VALUES ($1, $2, $3)
		ON CONFLICT (discord_id_hash) DO UPDATE
			SET last_seen      = NOW(),
			    discord_id     = EXCLUDED.discord_id,
			    discord_handle = EXCLUDED.discord_handle
		RETURNING anonymous_id, discord_id_hash, discord_id, discord_handle,
		          age_verified, verified_at, verification_provider,
		          push_token, server_member, created_at, last_seen
	`, discordIDHash, discordID, discordHandle)

	var u User
	if err := row.Scan(
		&u.AnonymousID, &u.DiscordIDHash, &u.DiscordID, &u.DiscordHandle,
		&u.AgeVerified, &u.VerifiedAt, &u.VerificationProvider,
		&u.PushToken, &u.ServerMember, &u.CreatedAt, &u.LastSeen,
	); err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}
	return &u, nil
}

func (d *DB) GetUserByAnonymousID(ctx context.Context, id string) (*User, error) {
	row := d.pool.QueryRow(ctx, `
		SELECT anonymous_id, discord_id_hash, discord_id,
		       age_verified, verified_at, verification_provider,
		       push_token, created_at, last_seen
		FROM users
		WHERE anonymous_id = $1
	`, id)

	var u User
	if err := row.Scan(
		&u.AnonymousID, &u.DiscordIDHash, &u.DiscordID,
		&u.AgeVerified, &u.VerifiedAt, &u.VerificationProvider,
		&u.PushToken, &u.CreatedAt, &u.LastSeen,
	); err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (d *DB) MarkAgeVerified(ctx context.Context, anonymousID, provider string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE users
		SET age_verified = true,
		    verified_at = NOW(),
		    verification_provider = $2
		WHERE anonymous_id = $1
	`, anonymousID, provider)
	return err
}

func (d *DB) UpdatePushToken(ctx context.Context, anonymousID, token string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE users SET push_token = $2 WHERE anonymous_id = $1
	`, anonymousID, token)
	return err
}

func (d *DB) DeleteUser(ctx context.Context, anonymousID string) error {
	// CASCADE handles agent_signals deletion
	_, err := d.pool.Exec(ctx, `
		DELETE FROM users WHERE anonymous_id = $1
	`, anonymousID)
	return err
}

// ─── Signal queries ───────────────────────────────────────────────────────────

type Signal struct {
	AnonymousID        string
	LocationH3         string
	Seeking            string
	AgeMin             int
	AgeMax             int
	PreferenceVector   []byte
	PublicKey          string
	Matched            bool
	CreatedAt          time.Time
	ExpiresAt          time.Time

	// Joined from users — populated in matching queries
	DiscordID     string
	DiscordHandle string
}

func (d *DB) UpsertSignal(ctx context.Context,
	anonymousID, locationH3, seeking string,
	ageMin, ageMax int,
	preferenceVector []byte,
	publicKey string,
	pushToken *string,
) (*Signal, error) {
	// Update push token if provided
	if pushToken != nil {
		_ = d.UpdatePushToken(ctx, anonymousID, *pushToken)
	}

	row := d.pool.QueryRow(ctx, `
		INSERT INTO agent_signals
			(anonymous_id, location_h3, seeking, age_min, age_max,
			 preference_vector, public_key, matched, expires_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, false, NOW() + INTERVAL '7 days')
		ON CONFLICT (anonymous_id) DO UPDATE SET
			location_h3       = EXCLUDED.location_h3,
			seeking           = EXCLUDED.seeking,
			age_min           = EXCLUDED.age_min,
			age_max           = EXCLUDED.age_max,
			preference_vector = EXCLUDED.preference_vector,
			public_key        = EXCLUDED.public_key,
			matched           = false,
			expires_at        = NOW() + INTERVAL '7 days'
		RETURNING anonymous_id, location_h3, seeking, age_min, age_max,
		          preference_vector, public_key, matched, created_at, expires_at
	`, anonymousID, locationH3, seeking, ageMin, ageMax, preferenceVector, publicKey)

	var s Signal
	if err := row.Scan(
		&s.AnonymousID, &s.LocationH3, &s.Seeking,
		&s.AgeMin, &s.AgeMax, &s.PreferenceVector, &s.PublicKey,
		&s.Matched, &s.CreatedAt, &s.ExpiresAt,
	); err != nil {
		return nil, fmt.Errorf("upsert signal: %w", err)
	}
	return &s, nil
}

func (d *DB) DeleteSignal(ctx context.Context, anonymousID string) error {
	_, err := d.pool.Exec(ctx, `
		DELETE FROM agent_signals WHERE anonymous_id = $1
	`, anonymousID)
	return err
}

// GetUnmatchedSignals returns all unmatched, unexpired, age-verified signals.
// Used by the matching job.
func (d *DB) GetUnmatchedSignals(ctx context.Context) ([]Signal, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT s.anonymous_id, s.location_h3, s.seeking,
		       s.age_min, s.age_max, s.preference_vector,
		       s.public_key, s.matched, s.created_at, s.expires_at,
		       u.discord_id, u.discord_handle
		FROM   agent_signals s
		JOIN   users u ON u.anonymous_id = s.anonymous_id
		WHERE  s.matched     = false
		AND    s.expires_at  > NOW()
		AND    u.age_verified = true
		ORDER  BY s.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("get unmatched signals: %w", err)
	}
	defer rows.Close()

	var signals []Signal
	for rows.Next() {
		var s Signal
		if err := rows.Scan(
			&s.AnonymousID, &s.LocationH3, &s.Seeking,
			&s.AgeMin, &s.AgeMax, &s.PreferenceVector, &s.PublicKey,
			&s.Matched, &s.CreatedAt, &s.ExpiresAt,
			&s.DiscordID, &s.DiscordHandle,
		); err != nil {
			return nil, err
		}
		signals = append(signals, s)
	}
	return signals, rows.Err()
}

func (d *DB) MarkSignalsMatched(ctx context.Context, idA, idB string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE agent_signals
		SET matched = true
		WHERE anonymous_id IN ($1, $2)
	`, idA, idB)
	return err
}

// CleanExpiredSignals removes signals past their expiry. Called hourly.
func (d *DB) CleanExpiredSignals(ctx context.Context) (int64, error) {
	result, err := d.pool.Exec(ctx, `
		DELETE FROM agent_signals WHERE expires_at < NOW()
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// ─── Match queries ────────────────────────────────────────────────────────────

type Match struct {
	ID               string
	UserA            string
	UserB            string
	DiscordChannelID string
	CreatedAt        time.Time
}

func (d *DB) CreateMatch(ctx context.Context,
	userA, userB, discordChannelID string,
) (*Match, error) {
	row := d.pool.QueryRow(ctx, `
		INSERT INTO matches (user_a, user_b, discord_channel_id)
		VALUES ($1, $2, $3)
		RETURNING id, user_a, user_b, discord_channel_id, created_at
	`, userA, userB, discordChannelID)

	var m Match
	if err := row.Scan(
		&m.ID, &m.UserA, &m.UserB, &m.DiscordChannelID, &m.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("create match: %w", err)
	}
	return &m, nil
}

func (d *DB) GetMatchesForUser(ctx context.Context, anonymousID string) ([]Match, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, user_a, user_b, discord_channel_id, created_at
		FROM   matches
		WHERE  user_a = $1 OR user_b = $1
		ORDER  BY created_at DESC
	`, anonymousID)
	if err != nil {
		return nil, fmt.Errorf("get matches: %w", err)
	}
	defer rows.Close()

	var matches []Match
	for rows.Next() {
		var m Match
		if err := rows.Scan(
			&m.ID, &m.UserA, &m.UserB, &m.DiscordChannelID, &m.CreatedAt,
		); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

// ─── Verification session queries ─────────────────────────────────────────────

func (d *DB) CreateVerificationSession(ctx context.Context,
	sessionID, anonymousID string,
) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO verification_sessions (session_id, anonymous_id)
		VALUES ($1, $2)
	`, sessionID, anonymousID)
	return err
}

func (d *DB) GetVerificationSession(ctx context.Context,
	sessionID string,
) (anonymousID string, err error) {
	row := d.pool.QueryRow(ctx, `
		SELECT anonymous_id
		FROM   verification_sessions
		WHERE  session_id = $1
		AND    expires_at > NOW()
	`, sessionID)
	err = row.Scan(&anonymousID)
	return
}

func (d *DB) DeleteVerificationSession(ctx context.Context, sessionID string) error {
	_, err := d.pool.Exec(ctx, `
		DELETE FROM verification_sessions WHERE session_id = $1
	`, sessionID)
	return err
}

func (d *DB) CleanExpiredVerifications(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `
		DELETE FROM verification_sessions WHERE expires_at < NOW()
	`)
	return err
}

// SignalActive returns whether a user has an active unmatched signal.
func (d *DB) SignalActive(ctx context.Context, anonymousID string) (bool, error) {
	var exists bool
	err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_signals
			WHERE anonymous_id = $1
			AND   matched     = false
			AND   expires_at  > NOW()
		)
	`, anonymousID).Scan(&exists)
	return exists, err
}
