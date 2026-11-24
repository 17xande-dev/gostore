-- +goose Up
-- The idle limit needs to know when a session was last used. last_seen is
-- written at most once a minute (internal/auth, session.NeedsTouch), so a busy
-- page does not write on every request.
--
-- The default is evaluated once, when the column is added, so every session that
-- exists at the migration is treated as seen at that moment: the column's arrival
-- signs nobody out. A new session starts seen at its creation, because the insert
-- leaves the column to the default. A binary from before this migration inserts
-- sessions without naming the column, which still works.
ALTER TABLE admin_sessions ADD COLUMN last_seen TIMESTAMPTZ NOT NULL DEFAULT now();

-- +goose Down
-- Rolling back forgets only when sessions were last used; every session row stays
-- valid under the absolute limit it was issued with.
ALTER TABLE admin_sessions DROP COLUMN last_seen;
