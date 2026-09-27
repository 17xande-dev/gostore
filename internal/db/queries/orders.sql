-- Queries behind internal/orders. See sqlc.yaml; regenerate with `make sqlc`.

-- The cart, priced for snapshotting into an order. Read inside the transaction
-- that creates the order, so the total is one the catalog agrees with right now
-- and not the figure the submitted page happened to be showing.
-- The ::bool cast is not redundant. Neither column is nullable, so the AND cannot
-- be NULL, but sqlc cannot prove that and would otherwise hand the store a *bool
-- — implying a third state that does not exist. The cast says so in SQL.
-- name: ListCartLinesForOrder :many
SELECT i.variant_id, i.quantity, p.title, p.kind, v.option1, v.option2, v.option3,
       v.price_cents, v.stock_qty, (v.active AND p.active)::bool AS purchasable
FROM cart_items i
JOIN product_variants v ON v.id = i.variant_id
JOIN products p ON p.id = v.product_id
WHERE i.cart_id = $1
ORDER BY p.title, v.option1, v.option2, v.option3, v.sku;

-- name: CreateOrder :one
INSERT INTO orders (id, cart_id, customer_name, customer_email, customer_phone,
                    shipping_address, total_cents, currency, status, gateway, cart_version, checkout_key)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, created_at;

-- name: GetCheckoutByKey :one
SELECT * FROM orders WHERE cart_id = $1 AND checkout_key = $2;

-- The snapshot: later catalog edits must never rewrite purchase history.
--
-- variant_label is the options already rendered — 'L / Navy' — rather than the
-- three values in their own columns, because this snapshot is read only for
-- display. Rendering it here rather than at read time also means a product that
-- later renames its option slots cannot relabel a completed sale.
-- name: CreateOrderItem :exec
INSERT INTO order_items (order_id, variant_id, title, variant_label, kind, unit_price_cents, quantity)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetLatestOrderForCart :one
SELECT * FROM orders WHERE cart_id = $1 ORDER BY created_at DESC LIMIT 1;

-- The admin's order list. Limited rather than unpaginated: products are a small
-- fixed set, but orders accumulate forever, so "the catalog is small" does not
-- carry over to this table. Newest first, because that is the one an operator is
-- looking for.
--
-- Whole rows and no aggregate, so sqlc reuses the Order model rather than
-- generating a near-identical row type that would need its own field-by-field
-- mapping. An item count would be nice on this page and is not worth that; the
-- lines are one click away.
-- name: ListRecentOrders :many
SELECT * FROM orders ORDER BY created_at DESC LIMIT $1;

-- name: SearchOrders :many
SELECT o.* FROM orders o
WHERE (sqlc.arg(search)::text = ''
       OR strpos(lower(o.id::text), lower(sqlc.arg(search))) > 0
       OR strpos(lower(o.customer_email), lower(sqlc.arg(search))) > 0
       OR strpos(lower(o.customer_name), lower(sqlc.arg(search))) > 0)
  AND (sqlc.arg(filter)::text = ''
       OR (sqlc.arg(filter) = 'oversold' AND o.oversold)
       OR (sqlc.arg(filter) = 'email' AND EXISTS
           (SELECT 1 FROM email_jobs e WHERE e.order_id = o.id AND e.sent_at IS NULL))
       OR (sqlc.arg(filter) = 'unfulfilled' AND o.status = 'paid' AND o.fulfilled_at IS NULL
           AND EXISTS (SELECT 1 FROM order_items i WHERE i.order_id = o.id AND i.kind = 'physical')))
ORDER BY o.created_at DESC, o.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- Payment facts are never written by the administrator.
-- name: UpdateOrderFulfillment :execrows
UPDATE orders SET
    fulfilled_at = CASE WHEN sqlc.arg(fulfilled)::bool THEN COALESCE(fulfilled_at, now()) ELSE NULL END,
    tracking_reference = sqlc.arg(tracking_reference), internal_note = sqlc.arg(internal_note),
    fulfillment_updated_at = now(), fulfillment_updated_by = sqlc.arg(actor)
WHERE orders.id = sqlc.arg(id) AND orders.status = 'paid'
  AND EXISTS (SELECT 1 FROM order_items i WHERE i.order_id = orders.id AND i.kind = 'physical');

-- name: ListOrderItems :many
SELECT id, variant_id, title, variant_label, kind, unit_price_cents, quantity
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

-- Set inside the same transaction as MarkOrderPaid when a stock decrement found
-- nothing left to decrement. The order stays paid; this is what tells a human.
-- name: FlagOrderOversold :exec
UPDATE orders SET oversold = TRUE WHERE id = $1;

-- The stock_qty >= $1 guard is what makes this safe rather than the transaction
-- alone: it turns "would go negative" into zero rows affected, a fact the caller
-- can act on, instead of a constraint violation that would abort a transaction
-- which has already taken money.
-- name: DecrementVariantStock :execrows
UPDATE product_variants SET stock_qty = stock_qty - $1 WHERE id = $2 AND stock_qty >= $1;

-- Only the unchanged purchased version is consumed. UPDATE takes the same cart
-- lock as every mutation, so a later edit cannot race the comparison and delete.
-- name: ClearCartForOrder :exec
WITH consumed AS (
    UPDATE carts c SET version = c.version + 1, updated_at = now()
    FROM orders o
    WHERE o.id = $1 AND c.id = o.cart_id AND c.version = o.cart_version
    RETURNING c.id
)
DELETE FROM cart_items WHERE cart_id IN (SELECT id FROM consumed);

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

-- The digital lines of a paid order, which are the ones that need entitlements.
-- Read inside MarkPaid's transaction, from the order_items snapshot rather than
-- from products, so a product flipped afterwards cannot change what an order
-- granted.
-- name: ListDigitalOrderItems :many
SELECT id, variant_id, title, variant_label
FROM order_items
WHERE order_id = $1 AND kind = 'digital'
ORDER BY id;
