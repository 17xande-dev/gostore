-- Queries behind internal/cart. See sqlc.yaml; regenerate with `make sqlc`.

-- name: CreateCart :exec
INSERT INTO carts (id) VALUES ($1);

-- name: GetCart :one
SELECT * FROM carts WHERE id = $1;

-- The join is deliberately not filtered by active: a line whose product has been
-- withdrawn stays visible and is marked unavailable, because a line silently
-- disappearing between page loads looks like a bug or a hidden price change.
--
-- Nothing here is stored on the cart row. Title, price and stock are read live, so
-- a price change or a sell-out shows up the next time the cart is looked at.
-- The ::bool cast is not redundant. Neither column is nullable, so the AND cannot
-- be NULL, but sqlc cannot prove that and would otherwise hand the store a *bool
-- — implying a third state that does not exist. The cast says so in SQL.
-- name: ListCartItems :many
SELECT i.variant_id, i.quantity,
       p.slug AS product_slug, p.title AS product_title, p.kind,
       v.sku, v.option1, v.option2, v.option3, v.price_cents, v.stock_qty,
       (v.active AND p.active)::bool AS purchasable
FROM cart_items i
JOIN product_variants v ON v.id = i.variant_id
JOIN products p ON p.id = v.product_id
WHERE i.cart_id = $1
ORDER BY p.title, v.option1, v.option2, v.option3, v.sku;

-- name: GetVariantAvailability :one
SELECT v.stock_qty, p.kind, (v.active AND p.active)::bool AS purchasable
FROM product_variants v
JOIN products p ON p.id = v.product_id
WHERE v.id = $1;

-- name: GetCartLineQuantity :one
SELECT quantity FROM cart_items WHERE cart_id = $1 AND variant_id = $2;

-- name: UpsertCartLine :exec
INSERT INTO cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = EXCLUDED.quantity;

-- Stamping the cart is what makes the cleanup job measure activity rather than
-- creation.
-- name: TouchCart :exec
UPDATE carts SET updated_at = now() WHERE id = $1;

-- name: DeleteCartLine :exec
DELETE FROM cart_items WHERE cart_id = $1 AND variant_id = $2;

-- name: ClearCart :exec
DELETE FROM cart_items WHERE cart_id = $1;

-- name: DeleteCartsOlderThan :execrows
DELETE FROM carts WHERE updated_at < now() - make_interval(days => $1);
