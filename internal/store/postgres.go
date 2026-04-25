package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/open-merge/broker/internal/model"
)

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

type PostgresStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
}

func NewPostgresStore(pool *pgxpool.Pool, ttl time.Duration) *PostgresStore {
	return &PostgresStore{pool: pool, ttl: ttl}
}

func (s *PostgresStore) UpsertSignal(ctx context.Context, signal *model.Signal) error {
	// Ensure user row exists (FK constraint)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (anonymous_id, discord_id_hash, discord_id)
		VALUES ($1, $2, $2)
		ON CONFLICT (anonymous_id) DO NOTHING
	`, signal.AnonymousID, signal.DiscordIDHash)
	if err != nil {
		return err
	}

	// Update push token on users if provided
	if signal.PushToken != nil {
		_, _ = s.pool.Exec(ctx, `
			UPDATE users SET push_token = $2 WHERE anonymous_id = $1
		`, signal.AnonymousID, *signal.PushToken)
	}

	// Decode encryptedVector from base64 if it's stored as string in the signal
	var prefVec []byte
	if len(signal.EncryptedVector) > 0 {
		prefVec = signal.EncryptedVector
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_signals
			(anonymous_id, signal_id, location_h3, gender, seeking, age, age_min, age_max,
			 preference_vector, public_key, matched, expires_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, false, NOW() + $11::interval)
		ON CONFLICT (anonymous_id) DO UPDATE SET
			signal_id         = EXCLUDED.signal_id,
			location_h3       = EXCLUDED.location_h3,
			gender            = EXCLUDED.gender,
			seeking           = EXCLUDED.seeking,
			age               = EXCLUDED.age,
			age_min           = EXCLUDED.age_min,
			age_max           = EXCLUDED.age_max,
			preference_vector = EXCLUDED.preference_vector,
			public_key        = EXCLUDED.public_key,
			matched           = false,
			expires_at        = NOW() + $11::interval
	`, signal.AnonymousID, signal.SignalID, signal.LocationH3, signal.Gender, signal.Seeking,
		signal.Age, signal.AgeMin, signal.AgeMax, prefVec, signal.PublicKey,
		s.ttl.String())
	return err
}

func (s *PostgresStore) GetSignalByID(ctx context.Context, signalID string) (*model.Signal, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT s.signal_id, s.anonymous_id, s.location_h3, s.seeking,
		       s.age_min, s.age_max, s.preference_vector, s.public_key,
		       s.matched, s.created_at, s.expires_at,
		       u.discord_id_hash, u.push_token
		FROM agent_signals s
		JOIN users u ON u.anonymous_id = s.anonymous_id
		WHERE s.signal_id = $1 AND s.expires_at > NOW()
	`, signalID)

	return s.scanSignal(row)
}

func (s *PostgresStore) GetSignalByAnonID(ctx context.Context, anonymousID string) (*model.Signal, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT s.signal_id, s.anonymous_id, s.location_h3, s.seeking,
		       s.age_min, s.age_max, s.preference_vector, s.public_key,
		       s.matched, s.created_at, s.expires_at,
		       u.discord_id_hash, u.push_token
		FROM agent_signals s
		JOIN users u ON u.anonymous_id = s.anonymous_id
		WHERE s.anonymous_id = $1 AND s.expires_at > NOW()
	`, anonymousID)

	return s.scanSignal(row)
}

func (s *PostgresStore) DeleteSignal(ctx context.Context, anonymousID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM agent_signals WHERE anonymous_id = $1
	`, anonymousID)
	return err
}

func (s *PostgresStore) DeleteUser(ctx context.Context, anonymousID string) error {
	// CASCADE on users table handles agent_signals and related rows
	_, err := s.pool.Exec(ctx, `
		DELETE FROM users WHERE anonymous_id = $1
	`, anonymousID)
	return err
}

func (s *PostgresStore) FindSignalsByH3Cells(ctx context.Context, cells []string) ([]model.DiscoveryResult, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.anonymous_id, s.location_h3, s.seeking,
		       s.age_min, s.age_max, s.preference_vector, s.public_key,
		       u.discord_id_hash
		FROM agent_signals s
		JOIN users u ON u.anonymous_id = s.anonymous_id
		WHERE s.location_h3 = ANY($1) AND s.expires_at > NOW() AND s.matched = false
	`, cells)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []model.DiscoveryResult
	for rows.Next() {
		var r model.DiscoveryResult
		var prefVec []byte
		if err := rows.Scan(
			&r.AnonymousID, &r.LocationH3, &r.Seeking,
			&r.AgeMin, &r.AgeMax, &prefVec, &r.PublicKey,
			&r.DiscordIDHash,
		); err != nil {
			return nil, err
		}
		r.EncryptedVector = prefVec
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *PostgresStore) CleanExpired(ctx context.Context) (int64, error) {
	result, err := s.pool.Exec(ctx, `
		DELETE FROM agent_signals WHERE expires_at < NOW()
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PostgresStore) scanSignal(row pgx.Row) (*model.Signal, error) {
	var sig model.Signal
	var prefVec []byte
	err := row.Scan(
		&sig.SignalID, &sig.AnonymousID, &sig.LocationH3, &sig.Seeking,
		&sig.AgeMin, &sig.AgeMax, &prefVec, &sig.PublicKey,
		&sig.Matched, &sig.CreatedAt, &sig.ExpiresAt,
		&sig.DiscordIDHash, &sig.PushToken,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	sig.EncryptedVector = prefVec
	return &sig, nil
}

func (s *PostgresStore) GetMatchesForUser(ctx context.Context, anonymousID string) ([]model.Match, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_a, user_b, discord_channel_id, created_at
		FROM   matches
		WHERE  user_a = $1 OR user_b = $1
		ORDER  BY created_at DESC
	`, anonymousID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []model.Match
	for rows.Next() {
		var m model.Match
		if err := rows.Scan(&m.ID, &m.UserA, &m.UserB, &m.DiscordChannelID, &m.CreatedAt); err != nil {
			return nil, err
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *PostgresStore) SignalActive(ctx context.Context, anonymousID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_signals
			WHERE anonymous_id = $1
			AND   matched     = false
			AND   expires_at  > NOW()
		)
	`, anonymousID).Scan(&exists)
	return exists, err
}

func (s *PostgresStore) EnsureUser(ctx context.Context, anonymousID string, discordIDHash string) (string, error) {
	// Try to find existing user by discord_id_hash first.
	// If found, update last_seen and return the existing anonymous_id
	// (the caller may have sent a fresh anonymous_id from a new install).
	var resolved string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (anonymous_id, discord_id_hash, discord_id)
		VALUES ($1, $2, $2)
		ON CONFLICT (discord_id_hash) DO UPDATE
			SET last_seen = NOW()
		RETURNING anonymous_id
	`, anonymousID, discordIDHash).Scan(&resolved)
	return resolved, err
}

// EnsureUserFromDiscord is like EnsureUser but stores the real discord_id and handle.
// Used by the /auth/discord endpoint where the broker exchanges the OAuth code
// server-side and therefore has the real Discord identity.
func (s *PostgresStore) EnsureUserFromDiscord(ctx context.Context, anonymousID, discordIDHash, discordID, discordHandle string) (string, error) {
	var resolved string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (anonymous_id, discord_id_hash, discord_id, discord_handle)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (discord_id_hash) DO UPDATE
			SET discord_id = EXCLUDED.discord_id,
			    discord_handle = EXCLUDED.discord_handle,
			    last_seen = NOW()
		RETURNING anonymous_id
	`, anonymousID, discordIDHash, discordID, discordHandle).Scan(&resolved)
	return resolved, err
}

func (s *PostgresStore) GetUnmatchedSignals(ctx context.Context) ([]model.UnmatchedSignal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.anonymous_id, s.location_h3, s.gender, s.seeking,
		       s.age, s.age_min, s.age_max, u.discord_id,
		       COALESCE(u.discord_handle, '')
		FROM   agent_signals s
		JOIN   users u ON u.anonymous_id = s.anonymous_id
		WHERE  s.matched    = false
		AND    s.expires_at > NOW()
		-- TODO: Add AND u.age_verified = true when verification feature ships
		ORDER  BY s.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []model.UnmatchedSignal
	for rows.Next() {
		var sig model.UnmatchedSignal
		if err := rows.Scan(
			&sig.AnonymousID, &sig.LocationH3, &sig.Gender, &sig.Seeking,
			&sig.Age, &sig.AgeMin, &sig.AgeMax, &sig.DiscordID,
			&sig.DiscordHandle,
		); err != nil {
			return nil, err
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

func (s *PostgresStore) MarkSignalsMatched(ctx context.Context, idA, idB string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE agent_signals SET matched = true WHERE anonymous_id IN ($1, $2)
	`, idA, idB)
	return err
}

func (s *PostgresStore) CreateMatch(ctx context.Context, userA, userB, discordChannelID string) (string, error) {
	var matchID string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO matches (user_a, user_b, discord_channel_id)
		VALUES ($1, $2, $3)
		RETURNING id
	`, userA, userB, discordChannelID).Scan(&matchID)
	return matchID, err
}

func (s *PostgresStore) CleanExpiredVerifications(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM verification_sessions WHERE expires_at < NOW()
	`)
	return err
}

func (s *PostgresStore) GetMatchUsersDiscord(ctx context.Context, matchID string) (discordIDA, discordIDB, handleA, handleB string, bothMembers bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT ua.discord_id, ub.discord_id,
		       COALESCE(ua.discord_handle, ''), COALESCE(ub.discord_handle, ''),
		       (ua.server_member AND ub.server_member)
		FROM   matches m
		JOIN   users ua ON ua.anonymous_id = m.user_a
		JOIN   users ub ON ub.anonymous_id = m.user_b
		WHERE  m.id = $1
	`, matchID).Scan(&discordIDA, &discordIDB, &handleA, &handleB, &bothMembers)
	return
}

func (s *PostgresStore) UpdateMatchChannel(ctx context.Context, matchID, channelID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE matches SET discord_channel_id = $2 WHERE id = $1
	`, matchID, channelID)
	return err
}

func (s *PostgresStore) GetMatchesMissingChannel(ctx context.Context) ([]model.MatchWithUsers, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.user_a, m.user_b,
		       ua.discord_id, ub.discord_id,
		       COALESCE(ua.discord_handle, ''), COALESCE(ub.discord_handle, '')
		FROM   matches m
		JOIN   users ua ON ua.anonymous_id = m.user_a
		JOIN   users ub ON ub.anonymous_id = m.user_b
		WHERE  m.discord_channel_id = ''
		AND    ua.server_member = true
		AND    ub.server_member = true
		ORDER  BY m.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []model.MatchWithUsers
	for rows.Next() {
		var m model.MatchWithUsers
		if err := rows.Scan(
			&m.MatchID, &m.UserA, &m.UserB,
			&m.DiscordIDA, &m.DiscordIDB,
			&m.HandleA, &m.HandleB,
		); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

func (s *PostgresStore) SetServerMember(ctx context.Context, discordID string, handle string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET server_member = true, discord_handle = $2 WHERE discord_id = $1
	`, discordID, handle)
	return err
}

func (s *PostgresStore) MarkAgeVerified(ctx context.Context, anonymousID, provider string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET age_verified = true,
		    verified_at = NOW(),
		    verification_provider = $2
		WHERE anonymous_id = $1
	`, anonymousID, provider)
	return err
}

// FindCandidatesForSignal returns unmatched signals that are compatible with
// the given signal, using DB-side filtering for seeking, age, and H3 location.
// The h3Cells slice should be the k-ring disk around the signal's cell.
// Rows are locked with FOR UPDATE SKIP LOCKED to prevent concurrent match races.
func (s *PostgresStore) FindCandidatesForSignal(ctx context.Context, signal *model.Signal, h3Cells []string) ([]model.UnmatchedSignal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.anonymous_id, s.location_h3, s.gender, s.seeking,
		       s.age, s.age_min, s.age_max, u.discord_id,
		       COALESCE(u.discord_handle, '')
		FROM   agent_signals s
		JOIN   users u ON u.anonymous_id = s.anonymous_id
		WHERE  s.matched    = false
		AND    s.expires_at > NOW()
		AND    s.anonymous_id != $1
		AND    s.location_h3 = ANY($2)
		-- gender-based seeking: candidate's gender matches our seeking, and our gender matches candidate's seeking
		AND    (s.gender = $3 OR $3 = 'any')
		AND    ($4 = s.seeking OR s.seeking = 'any')
		-- mutual age check: candidate's actual age in our range, our age in candidate's range
		AND    s.age >= $5 AND s.age <= $6
		AND    $7 >= s.age_min AND $7 <= s.age_max
		ORDER  BY s.created_at ASC
		FOR UPDATE OF s SKIP LOCKED
	`, signal.AnonymousID, h3Cells, signal.Seeking, signal.Gender, signal.AgeMin, signal.AgeMax, signal.Age)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []model.UnmatchedSignal
	for rows.Next() {
		var c model.UnmatchedSignal
		if err := rows.Scan(
			&c.AnonymousID, &c.LocationH3, &c.Gender, &c.Seeking,
			&c.Age, &c.AgeMin, &c.AgeMax, &c.DiscordID,
			&c.DiscordHandle,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// CreateMatchIfNotExists inserts a match only if the pair doesn't already exist.
// Returns the match ID, whether it was newly created, and any error.
func (s *PostgresStore) CreateMatchIfNotExists(ctx context.Context, userA, userB, discordChannelID string) (string, bool, error) {
	var matchID string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO matches (user_a, user_b, discord_channel_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (LEAST(user_a, user_b), GREATEST(user_a, user_b)) DO NOTHING
		RETURNING id
	`, userA, userB, discordChannelID).Scan(&matchID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already exists — not an error, just a no-op
			return "", false, nil
		}
		return "", false, err
	}
	return matchID, true, nil
}

func GenerateSignalID() string {
	const idLen = 12
	b := make([]byte, idLen)
	max := big.NewInt(int64(len(base62Chars)))
	for i := range b {
		n, _ := rand.Int(rand.Reader, max)
		b[i] = base62Chars[n.Int64()]
	}
	return "sig_" + string(b)
}

// DecodeEncryptedVector decodes a base64 string to bytes for storage.
func DecodeEncryptedVector(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
