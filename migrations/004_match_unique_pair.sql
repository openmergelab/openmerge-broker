-- 004_match_unique_pair.sql
-- Prevent duplicate matches between the same pair of users.
-- LEAST/GREATEST normalises the order so (A,B) and (B,A) are the same.

CREATE UNIQUE INDEX IF NOT EXISTS idx_matches_unique_pair
    ON matches (LEAST(user_a, user_b), GREATEST(user_a, user_b));
