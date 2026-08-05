-- +goose Up
-- image_url is redundant now that a product image is always an object this store
-- holds: the URL is image_key resolved against whichever backend is configured —
-- "/images/<key>" for a local directory, "<public base>/<key>" for a bucket.
--
-- Storing it as well made two things possible that should not be. The columns could
-- disagree. And, worse, the URL was baked into every row, so moving a store from a
-- local directory to R2 — or simply putting a custom domain in front of the bucket —
-- needed an UPDATE across the table before any image would load again. Computing it
-- at render time makes the backend a runtime concern, which is what it is: the same
-- rows serve a development machine and production.
ALTER TABLE products DROP COLUMN image_url;

-- +goose Down
-- The values cannot be restored, because they were never the source of truth: they
-- were a rendering of image_key against a backend this migration does not know. A
-- store rolled back this far re-derives them by running the newer code.
ALTER TABLE products ADD COLUMN image_url TEXT NOT NULL DEFAULT '';
