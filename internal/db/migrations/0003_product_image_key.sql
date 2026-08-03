-- +goose Up
-- image_key names the object in blob storage that this product's image *is*, as
-- opposed to image_url which is only where to fetch it from.
--
-- The two are deliberately distinct states rather than one field doing both jobs:
--
--   image_key = ''  the image_url was pasted in by hand. The store does not own
--                   the bytes and must never try to delete them.
--   image_key ≠ ''  the store uploaded this object and owns it, so replacing or
--                   removing the image has to delete it too, or storage fills up
--                   with objects nothing references.
--
-- Deriving the key by stripping a prefix off image_url would collapse that
-- distinction, and the first pasted URL that happened to live under the same
-- prefix would make the store delete somebody else's file.
ALTER TABLE products ADD COLUMN image_key TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE products DROP COLUMN image_key;
