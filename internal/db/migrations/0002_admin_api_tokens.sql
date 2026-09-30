-- +goose Up
-- API tokens: an administrator's credential for a program acting as them — the
-- MCP endpoint's bearer token. Shaped like admin_sessions on purpose: only
-- sha256(token) is stored, the row is what makes it revocable, and expiry is
-- enforced in the lookup's predicate. A token is its account acting, so it
-- carries no role or scope of its own; the account's current role is read on
-- every request, and a password change, disable or role change deletes these
-- rows in the same transaction that deletes the account's sessions.
CREATE TABLE admin_api_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash   BYTEA NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX admin_api_tokens_user_idx ON admin_api_tokens (user_id);

-- +goose Down
DROP TABLE admin_api_tokens;
