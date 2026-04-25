-- 006_add_age_to_signals.sql
-- Store the user's actual age on the signal so the matching job can
-- check "is user A's age within user B's preferred range?" rather than
-- just comparing whether two preference ranges overlap.

ALTER TABLE agent_signals
    ADD COLUMN age INT NOT NULL DEFAULT 18 CHECK (age >= 18 AND age <= 120);

-- Remove the default after backfill so future inserts must supply a value.
-- ALTER TABLE agent_signals ALTER COLUMN age DROP DEFAULT;
