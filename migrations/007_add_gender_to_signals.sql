-- 007_add_gender_to_signals.sql
-- Store the user's actual gender on the signal so matching can check
-- "does A's gender match what B is seeking?" rather than just comparing
-- what both users are seeking.

ALTER TABLE agent_signals
    ADD COLUMN gender TEXT NOT NULL DEFAULT 'any' CHECK (gender IN ('M', 'F', 'NB', 'any'));
