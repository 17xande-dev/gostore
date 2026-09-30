-- name: CreateAdminAPIToken :one
INSERT INTO admin_api_tokens (user_id, name, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- The bearer lookup. Everything that makes a token unusable is in the predicate,
-- for the same reason GetAdminSession's expiry is: a caller cannot forget a check
-- the query has already made. A disabled account, or one that must change its
-- password first, gets no programmatic access — the browser would bounce either
-- to a page, and a program has no page to be bounced to.
-- name: GetAdminAPIToken :one
SELECT sqlc.embed(t), sqlc.embed(u)
FROM admin_api_tokens t
JOIN admin_users u ON u.id = t.user_id
WHERE t.token_hash = $1
  AND t.expires_at > now()
  AND NOT u.disabled
  AND NOT u.must_change_password;

-- Throttled in the predicate, so a busy client costs one write a minute rather
-- than one per call, and the caller need not remember when it last wrote.
-- name: TouchAdminAPIToken :exec
UPDATE admin_api_tokens SET last_used_at = now()
WHERE id = $1
  AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ListAdminAPITokensForUser :many
SELECT * FROM admin_api_tokens
WHERE user_id = $1
ORDER BY created_at DESC;

-- Scoped to the owner, so revoking by id cannot reach somebody else's token.
-- name: DeleteAdminAPIToken :execrows
DELETE FROM admin_api_tokens WHERE id = $1 AND user_id = $2;

-- Runs beside DeleteAdminSessionsForUser, in the same transaction, whenever an
-- account's password, role or enabled state changes.
-- name: DeleteAdminAPITokensForUser :execrows
DELETE FROM admin_api_tokens WHERE user_id = $1;

-- name: DeleteExpiredAdminAPITokens :execrows
DELETE FROM admin_api_tokens WHERE expires_at < now();
