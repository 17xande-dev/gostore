-- Queries behind internal/catalog. See sqlc.yaml; regenerate with `make sqlc`.
--
-- `SELECT *` is deliberate on the single-table queries: sqlc expands it against
-- the real schema at generation time, so the column list in the generated code
-- cannot drift from the table the way a hand-maintained one could.

-- name: ListProducts :many
SELECT * FROM products ORDER BY title;

-- name: ListAllVariants :many
SELECT * FROM product_variants ORDER BY size, color, sku;

-- The products a customer may see: active, with at least one active variant. A
-- product with nothing purchasable under it is not a listing, it is a dead end.
-- name: ListActiveProducts :many
SELECT * FROM products p
WHERE p.active
  AND EXISTS (SELECT 1 FROM product_variants v WHERE v.product_id = p.id AND v.active)
ORDER BY p.title;

-- Out-of-stock variants are included. Hiding them makes a size silently vanish
-- from a selector, which reads as a bug; the storefront marks them unavailable.
-- name: ListActiveVariants :many
SELECT * FROM product_variants
WHERE active AND product_id IN (SELECT id FROM products WHERE active)
ORDER BY size, color, sku;

-- name: GetActiveProductBySlug :one
SELECT * FROM products WHERE slug = $1 AND active;

-- name: ListActiveVariantsByProduct :many
SELECT * FROM product_variants
WHERE product_id = $1 AND active
ORDER BY size, color, sku;

-- name: GetProduct :one
SELECT * FROM products WHERE id = $1;

-- name: GetProductBySlug :one
SELECT * FROM products WHERE slug = $1;

-- name: ListVariantsByProduct :many
SELECT * FROM product_variants WHERE product_id = $1 ORDER BY size, color, sku;

-- The database generates the id, which is why no UUID library reaches the binary.
--
-- image_url is absent: a new product has no image, and one only ever arrives by
-- upload through SetProductImage. There is no way to point a product at bytes this
-- store does not hold.
-- name: CreateProduct :one
INSERT INTO products (id, kind, slug, title, description, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
RETURNING *;

-- updated_at is maintained here rather than by a trigger, so the write is visible
-- in the query itself.
--
-- Neither image column appears here: the product form does not touch the image, and
-- an image is only ever set by SetProductImage or cleared by ClearProductImage.
-- That is what makes an empty submission from a form that does not render an image
-- field harmless, rather than a silent way to blank the picture.
-- name: UpdateProduct :one
UPDATE products
SET kind = $2, slug = $3, title = $4, description = $5, active = $6, updated_at = now()
WHERE id = $1
RETURNING *;

-- Points a product at an uploaded object. The caller deletes the previous object,
-- if there was one, *after* this commits: an orphaned object costs a few kilobytes,
-- while a deleted object still referenced by a live row is a broken image.
-- name: SetProductImage :one
UPDATE products
SET image_url = $2, image_key = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClearProductImage :one
UPDATE products
SET image_url = '', image_key = '', updated_at = now()
WHERE id = $1
RETURNING *;

-- Fails once an order references a variant of this product, because purchase
-- history must not be rewritable — deactivate instead.
-- name: DeleteProduct :execrows
DELETE FROM products WHERE id = $1;

-- name: CreateVariant :one
INSERT INTO product_variants (id, product_id, sku, size, color, price_cents, stock_qty, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- The product id is part of the WHERE clause, so a mismatched pair from a URL
-- updates nothing instead of editing another product's variant.
-- name: UpdateVariant :one
UPDATE product_variants
SET sku = $3, size = $4, color = $5, price_cents = $6, stock_qty = $7, active = $8
WHERE id = $1 AND product_id = $2
RETURNING *;

-- name: DeleteVariant :execrows
DELETE FROM product_variants WHERE id = $1 AND product_id = $2;

-- Upserts by natural key — slug for a product, SKU for a variant — which is what
-- makes cmd/seed rerunnable.
-- image_url is absent here too, so a seed file cannot claim a product's image. A
-- fixture has no way to upload bytes, so the only thing it could set is a URL
-- pointing somewhere this store does not control — which is exactly what is no
-- longer allowed. Re-seeding therefore leaves an uploaded image alone.
-- name: UpsertProduct :one
INSERT INTO products (id, kind, slug, title, description, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5)
ON CONFLICT (slug) DO UPDATE
SET kind = EXCLUDED.kind, title = EXCLUDED.title, description = EXCLUDED.description,
    active = EXCLUDED.active, updated_at = now()
RETURNING *;

-- stock_qty is deliberately absent from the DO UPDATE list: a seed file is a
-- starting point, not the truth about inventory somebody has since counted.
-- name: UpsertVariant :one
INSERT INTO product_variants (id, product_id, sku, size, color, price_cents, stock_qty, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (sku) DO UPDATE
SET product_id = EXCLUDED.product_id, size = EXCLUDED.size, color = EXCLUDED.color,
    price_cents = EXCLUDED.price_cents, active = EXCLUDED.active
RETURNING *;
