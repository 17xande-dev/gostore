-- name: CreateAdminImageUpload :exec
INSERT INTO admin_image_uploads (token_hash, api_token_id, product_id, expires_at)
VALUES ($1, $2, $3, $4);

-- The upload's lookup. Everything that makes it unusable is in the predicate, as
-- for the API token it came from: the upload's own expiry, the token's, and the
-- account's state. The role comes back so the caller can check it against the
-- permission an upload needs, which is a rule in Go rather than in SQL.
-- name: GetAdminImageUpload :one
SELECT iu.product_id, iu.api_token_id, u.id AS user_id, u.role
FROM admin_image_uploads iu
JOIN admin_api_tokens t ON t.id = iu.api_token_id
JOIN admin_users u ON u.id = t.user_id
WHERE iu.token_hash = $1
  AND iu.expires_at > now()
  AND t.expires_at > now()
  AND NOT u.disabled
  AND NOT u.must_change_password;

-- Spends an upload: one DELETE, so two requests racing with the same URL cannot
-- both win. The lookup's account checks are applied to what it deleted, so an
-- upload whose token or account went stale since the lookup comes back as no
-- row — and is spent anyway, which costs nothing, because it could never have
-- been used again.
-- name: ConsumeAdminImageUpload :one
WITH spent AS (
    DELETE FROM admin_image_uploads
    WHERE admin_image_uploads.token_hash = $1
      AND admin_image_uploads.expires_at > now()
    RETURNING admin_image_uploads.product_id, admin_image_uploads.api_token_id
)
SELECT spent.product_id, spent.api_token_id, u.id AS user_id, u.role
FROM spent
JOIN admin_api_tokens t ON t.id = spent.api_token_id
JOIN admin_users u ON u.id = t.user_id
WHERE t.expires_at > now()
  AND NOT u.disabled
  AND NOT u.must_change_password;

-- name: DeleteExpiredAdminImageUploads :execrows
DELETE FROM admin_image_uploads WHERE expires_at < now();
