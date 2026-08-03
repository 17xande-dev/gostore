-- Queries behind internal/orders. See sqlc.yaml; regenerate with `make sqlc`.

-- The cart, priced for snapshotting into an order. Read inside the transaction
-- that creates the order, so the total is one the catalog agrees with right now
-- and not the figure the submitted page happened to be showing.
-- The ::bool cast is not redundant. Neither column is nullable, so the AND cannot
-- be NULL, but sqlc cannot prove that and would otherwise hand the store a *bool
-- — implying a third state that does not exist. The cast says so in SQL.
-- name: ListCartLinesForOrder :many
SELECT i.variant_id, i.quantity, p.title, v.size, v.color,
       v.price_cents, v.stock_qty, (v.active AND p.active)::bool AS purchasable
FROM cart_items i
JOIN product_variants v ON v.id = i.variant_id
JOIN products p ON p.id = v.product_id
WHERE i.cart_id = $1
ORDER BY p.title, v.size, v.color, v.sku;

-- name: CreateOrder :one
INSERT INTO orders (id, cart_id, customer_name, customer_email, customer_phone,
                    shipping_address, total_cents, currency, status, gateway)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, created_at;

-- The snapshot: later catalog edits must never rewrite purchase history.
-- name: CreateOrderItem :exec
INSERT INTO order_items (order_id, variant_id, title, size, color, unit_price_cents, quantity)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetLatestOrderForCart :one
SELECT * FROM orders WHERE cart_id = $1 ORDER BY created_at DESC LIMIT 1;

-- name: ListOrderItems :many
SELECT variant_id, title, size, color, unit_price_cents, quantity
FROM order_items WHERE order_id = $1 ORDER BY id;

-- FOR UPDATE is the whole safety mechanism of MarkPaid: it serialises concurrent
-- notifications for one order, so two cannot each read the old stock and each
-- subtract from it.
-- name: LockOrderStatus :one
SELECT status FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderStatus :one
SELECT status FROM orders WHERE id = $1;

-- name: MarkOrderPaid :exec
UPDATE orders
SET status = $2, paid_at = now(), gateway = $3, gateway_ref = $4,
    gateway_status = $5, gateway_amount = $6, gateway_payload = $7
WHERE id = $1;

-- The stock_qty >= $1 guard is what makes this safe rather than the transaction
-- alone: it turns "would go negative" into zero rows affected, a fact the caller
-- can act on, instead of a constraint violation that would abort a transaction
-- which has already taken money.
-- name: DecrementVariantStock :execrows
UPDATE product_variants SET stock_qty = stock_qty - $1 WHERE id = $2 AND stock_qty >= $1;

-- The basket has become an order, so empty it. The cart row itself stays, so the
-- shopper's cookie keeps working for their next visit.
-- `orders.id` is qualified deliberately: cart_items has an id column too, so a
-- bare `id` here relies on Postgres resolving the innermost scope. It does, and
-- sqlc refuses to guess — which is the better position of the two.
-- name: ClearCartForOrder :exec
DELETE FROM cart_items WHERE cart_id = (SELECT cart_id FROM orders WHERE orders.id = $1);

-- Never contradicts a payment: a late failure notification arriving after a
-- genuine completion must not un-sell something already being packed.
-- name: RecordUnpaidOrder :execrows
UPDATE orders
SET status = $2, gateway = $3, gateway_ref = $4, gateway_status = $5,
    gateway_amount = $6, gateway_payload = $7
WHERE id = $1 AND status <> $8;

-- Records what a gateway said without touching the status, for a notification that
-- is genuine but cannot be acted on — the amount paid not matching the amount
-- asked for, above all.
-- name: RecordOrderNotification :execrows
UPDATE orders
SET gateway = $2, gateway_ref = $3, gateway_status = $4,
    gateway_amount = $5, gateway_payload = $6
WHERE id = $1;

-- name: MarkOrderEmailed :exec
UPDATE orders SET emailed = TRUE WHERE id = $1;
