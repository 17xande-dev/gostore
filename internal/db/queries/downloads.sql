-- Queries behind internal/downloads. See sqlc.yaml; regenerate with `make sqlc`.
--
-- An entitlement is one buyer's right to one purchased digital line. Revoking one
-- disables that buyer and nobody else, which is the whole reason the table exists
-- rather than a flag on the order.

-- Minted inside MarkPaid's transaction, so an order is never paid-without-
-- entitlements. Only the hash is stored; the plaintext token is returned to the
-- caller once, to go into the confirmation email, and is unrecoverable after that.
-- name: CreateEntitlement :one
INSERT INTO entitlements (order_id, order_item_id, variant_id, token_hash)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- The whole of the download authorisation, in one round trip.
--
-- Looked up by hash rather than by the token, so what is in the database is not
-- the credential. revoked_at comes back rather than being filtered out in SQL,
-- because "revoked" and "never existed" want different answers: the first can say
-- so, the second must not confirm that a token was ever valid.
-- name: GetEntitlementByTokenHash :one
SELECT e.*, p.id AS product_id, p.title AS product_title, p.slug AS product_slug,
       o.status AS order_status, o.customer_email
FROM entitlements e
JOIN product_variants v ON v.id = e.variant_id
JOIN products p ON p.id = v.product_id
JOIN orders o ON o.id = e.order_id
WHERE e.token_hash = $1;

-- One file of one entitlement, checked in the same statement that finds it: the
-- join through variant_files is what stops a buyer of the audio variant reading a
-- file id belonging to the video one out of another page and fetching it.
-- name: GetEntitlementFile :one
SELECT f.*
FROM product_files f
JOIN variant_files vf ON vf.file_id = f.id
WHERE vf.variant_id = $1 AND f.id = $2;

-- name: RecordDownload :exec
INSERT INTO download_events (entitlement_id, file_id, ip, user_agent)
VALUES ($1, $2, $3, $4);

-- name: RevokeEntitlement :execrows
UPDATE entitlements SET revoked_at = now()
WHERE id = $1 AND order_id = $2 AND revoked_at IS NULL;

-- name: RestoreEntitlement :execrows
UPDATE entitlements SET revoked_at = NULL
WHERE id = $1 AND order_id = $2 AND revoked_at IS NOT NULL;

-- The entitlements on one order, for the admin's revoke buttons, with how many
-- times each has been used. An operator deciding whether to revoke wants to know
-- whether the file has already been taken.
-- name: ListOrderEntitlements :many
SELECT e.id, e.variant_id, e.revoked_at, e.created_at,
       p.title AS product_title, oi.variant_label,
       (SELECT COUNT(*) FROM download_events d WHERE d.entitlement_id = e.id) AS download_count
FROM entitlements e
JOIN order_items oi ON oi.id = e.order_item_id
JOIN product_variants v ON v.id = e.variant_id
JOIN products p ON p.id = v.product_id
WHERE e.order_id = $1
ORDER BY p.title, oi.variant_label;

-- Product-level download statistics.
--
-- Counted from download_events rather than read back from the bucket, and that is
-- not a workaround: neither GCS nor R2 exposes per-object read counts, and a
-- presigned URL is anonymous to the bucket, so it could not attribute a download
-- to a buyer even if it counted one.
-- name: ProductDownloadStats :one
SELECT
    (SELECT COUNT(*) FROM download_events d
     JOIN product_files f ON f.id = d.file_id
     WHERE f.product_id = $1) AS total_downloads,
    (SELECT COUNT(DISTINCT d.entitlement_id) FROM download_events d
     JOIN product_files f ON f.id = d.file_id
     WHERE f.product_id = $1) AS unique_buyers,
    (SELECT MAX(d.created_at) FROM download_events d
     JOIN product_files f ON f.id = d.file_id
     WHERE f.product_id = $1) AS last_download,
    (SELECT COUNT(*) FROM entitlements e
     JOIN product_variants v ON v.id = e.variant_id
     WHERE v.product_id = $1) AS entitlements_issued,
    (SELECT COUNT(*) FROM entitlements e
     JOIN product_variants v ON v.id = e.variant_id
     WHERE v.product_id = $1 AND e.revoked_at IS NOT NULL) AS entitlements_revoked;

-- Per-file counts, LEFT JOINed so a file nobody has taken appears with a zero
-- rather than vanishing. A missing row reads as "no such file", which is the
-- wrong answer to "how many times was this downloaded".
-- name: ProductFileDownloadStats :many
SELECT f.id, f.title, f.size_bytes,
       COUNT(d.id) AS download_count,
       MAX(d.created_at) AS last_download
FROM product_files f
LEFT JOIN download_events d ON d.file_id = f.id
WHERE f.product_id = $1
GROUP BY f.id, f.title, f.size_bytes, f.position
ORDER BY f.position, f.id;

-- The most recent downloads of one product, so an operator looking at a suspicious
-- count can see the shape of it rather than only the total.
-- name: RecentProductDownloads :many
SELECT d.created_at, d.ip, f.title AS file_title, e.id AS entitlement_id,
       o.customer_email, o.id AS order_id
FROM download_events d
JOIN product_files f ON f.id = d.file_id
JOIN entitlements e ON e.id = d.entitlement_id
JOIN orders o ON o.id = e.order_id
WHERE f.product_id = $1
ORDER BY d.created_at DESC
LIMIT $2;

-- One entitlement of one order, for the post-payment page.
--
-- Keyed by order rather than by token, because the buyer standing on that page
-- has been identified by their cart cookie instead. Both are credentials for the
-- same thing — this person's own purchase — and neither is a session.
-- name: GetOrderEntitlement :one
SELECT e.*, p.id AS product_id, p.title AS product_title, p.slug AS product_slug,
       o.status AS order_status, o.customer_email
FROM entitlements e
JOIN product_variants v ON v.id = e.variant_id
JOIN products p ON p.id = v.product_id
JOIN orders o ON o.id = e.order_id
WHERE e.id = $1 AND e.order_id = $2;
