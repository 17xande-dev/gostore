-- Queries behind internal/catalog. See sqlc.yaml; regenerate with `make sqlc`.
--
-- `SELECT *` is deliberate on the single-table queries: sqlc expands it against
-- the real schema at generation time, so the column list in the generated code
-- cannot drift from the table the way a hand-maintained one could.

-- name: ListProducts :many
SELECT * FROM products ORDER BY title;

-- name: ListAllVariants :many
SELECT * FROM product_variants ORDER BY size, color, sku;

-- One statement serves search, category filtering and pagination, because they
-- are one question — "which products, in what order, and which slice of them" —
-- and asking it three times would let the three answers disagree.
--
-- COUNT(*) OVER () is the part worth naming: the size of the filtered set arrives
-- in the same round trip as the page, computed before LIMIT applies. A separate
-- COUNT would be a second trip whose answer can differ from the first under
-- concurrent edits, which shows up as page numbers promising results that are not
-- there.
--
-- Full-text and trigram are both here because neither is enough alone.
-- websearch_to_tsquery stems, so "books" reaches a title containing "book", but it
-- dies on a typo; trigram survives the typo and has no idea the two words are
-- related. ILIKE catches the third case both miss — a substring inside a word,
-- which is what a shopper typing half a title is doing.
--
-- An empty q needs no special case: websearch_to_tsquery('english','') is an empty
-- tsquery and similarity(title,'') is 0, so GREATEST is 0 for every row and the
-- ORDER BY falls through to p.title. A filter-only request is alphabetical for
-- free, with no second query and no branch in the handler. Passing '' rather than
-- NULL also keeps sqlc from handing the store a *string.
-- name: SearchActiveProducts :many
SELECT p.*, COUNT(*) OVER () AS total_count
FROM products p
WHERE p.active
  AND EXISTS (SELECT 1 FROM product_variants v
              WHERE v.product_id = p.id AND v.active)
  AND (cardinality(@category_slugs::text[]) = 0
       OR EXISTS (SELECT 1 FROM product_categories pc
                  JOIN categories c ON c.id = pc.category_id
                  WHERE pc.product_id = p.id
                    AND c.slug = ANY(@category_slugs::text[])))
  AND (@q::text = ''
       OR p.search @@ websearch_to_tsquery('english', @q)
       OR @q <% p.title
       OR p.title ILIKE '%' || @q || '%')
ORDER BY GREATEST(ts_rank_cd(p.search, websearch_to_tsquery('english', @q)),
                  similarity(p.title, @q)) DESC,
         p.title
LIMIT @page_size OFFSET @page_offset;

-- The variants for one page of products, and only that page: reading every active
-- variant in the catalog to render 24 products would undo the paging.
--
-- Out-of-stock variants are included. Hiding them makes a size silently vanish
-- from a selector, which reads as a bug; the storefront marks them unavailable.
-- name: ListActiveVariantsByProducts :many
SELECT * FROM product_variants
WHERE active AND product_id = ANY(@product_ids::uuid[])
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
-- A new product has no image; one only ever arrives by upload through
-- SetProductImage. There is no way to point a product at bytes this store does not
-- hold.
-- name: CreateProduct :one
INSERT INTO products (id, slug, title, description, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4)
RETURNING *;

-- updated_at is maintained here rather than by a trigger, so the write is visible
-- in the query itself.
--
-- image_key does not appear here: the product form does not touch the image, and an
-- image is only ever set by SetProductImage or cleared by ClearProductImage. That is
-- what makes a submission from a form with no image field harmless, rather than a
-- silent way to blank the picture.
-- name: UpdateProduct :one
UPDATE products
SET slug = $2, title = $3, description = $4, active = $5, updated_at = now()
WHERE id = $1
RETURNING *;

-- Points a product at an uploaded object. The caller deletes the previous object,
-- if there was one, *after* this commits: an orphaned object costs a few kilobytes,
-- while a deleted object still referenced by a live row is a broken image.
--
-- Only the key is stored. The URL it is served at depends on which backend is
-- configured and is computed when a page is rendered, so the same row works on a
-- development machine and in production.
-- name: SetProductImage :one
UPDATE products
SET image_key = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ClearProductImage :one
UPDATE products
SET image_key = '', updated_at = now()
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
-- No image column here either, so a seed file cannot claim a product's image: a
-- fixture has no way to upload bytes. Re-seeding leaves an uploaded image alone.
-- name: UpsertProduct :one
INSERT INTO products (id, slug, title, description, active)
VALUES (gen_random_uuid(), $1, $2, $3, $4)
ON CONFLICT (slug) DO UPDATE
SET title = EXCLUDED.title, description = EXCLUDED.description,
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

-- Categories. Ordered by position everywhere they are read, because the order a
-- shop offers its sections in is the owner's decision; name is only the tiebreak
-- so two categories at the same position still come back in a stable order.
-- name: ListCategories :many
SELECT * FROM categories ORDER BY position, name;

-- name: GetCategory :one
SELECT * FROM categories WHERE id = $1;

-- name: CreateCategory :one
INSERT INTO categories (id, slug, name, position)
VALUES (gen_random_uuid(), $1, $2, $3)
RETURNING *;

-- name: UpdateCategory :one
UPDATE categories SET slug = $2, name = $3, position = $4 WHERE id = $1 RETURNING *;

-- Deleting a category unlinks it from its products by cascade on the join table
-- and never touches the products themselves: a taxonomy edit must not delete
-- catalog entries.
-- name: DeleteCategory :execrows
DELETE FROM categories WHERE id = $1;

-- How many products a delete would unlink, read before the delete. Without it a
-- category that nothing used and one that fifty products used both look like the
-- same silent success.
-- name: CountCategoryProducts :one
SELECT COUNT(*) FROM product_categories WHERE category_id = $1;

-- ON CONFLICT DO UPDATE rather than DO NOTHING, because DO NOTHING returns no row
-- on conflict and the caller needs the id either way. slug = EXCLUDED.slug is a
-- write of the value that is already there; name and position are deliberately
-- left alone, so re-seeding never overwrites a category the operator has renamed
-- or reordered.
-- name: UpsertCategory :one
INSERT INTO categories (id, slug, name, position)
VALUES (gen_random_uuid(), $1, $2, $3)
ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
RETURNING *;

-- name: ListCategoriesByProduct :many
SELECT c.* FROM categories c
JOIN product_categories pc ON pc.category_id = c.id
WHERE pc.product_id = $1
ORDER BY c.position, c.name;

-- Every product's categories in one query, for the admin list to attach in
-- memory the way it already does with variants.
-- name: ListAllProductCategories :many
SELECT pc.product_id, sqlc.embed(c) FROM product_categories pc
JOIN categories c ON c.id = pc.category_id
ORDER BY c.position, c.name;

-- Setting a product's categories is a clear followed by inserts, inside the
-- transaction the store opens: the form submits the whole set, so anything not
-- resubmitted was unticked.
-- name: ClearProductCategories :exec
DELETE FROM product_categories WHERE product_id = $1;

-- The SELECT ... WHERE EXISTS makes an unknown category id insert nothing rather
-- than raise a foreign key violation. The handler validates ids against the list
-- it rendered, so this is the second line of defence, and the quiet one is right
-- here: a category deleted between rendering the form and submitting it should
-- not turn a product save into an error page.
-- name: AddProductCategory :exec
INSERT INTO product_categories (product_id, category_id)
SELECT $1, $2 WHERE EXISTS (SELECT 1 FROM categories WHERE id = $2)
ON CONFLICT DO NOTHING;

-- name: AddProductCategoryBySlug :exec
INSERT INTO product_categories (product_id, category_id)
SELECT $1, c.id FROM categories c WHERE c.slug = $2
ON CONFLICT DO NOTHING;
