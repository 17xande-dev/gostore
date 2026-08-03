-- +goose Up
-- A variant sitting in somebody's abandoned cart must not stop the shop owner
-- from deleting it. Carts are ephemeral, so the row goes with the variant.
--
-- order_items deliberately keeps its restricting reference: an order is a record
-- of what was actually bought, and deleting a variant must never quietly rewrite
-- purchase history. The two tables look similar and want opposite behaviour.
ALTER TABLE cart_items DROP CONSTRAINT cart_items_variant_id_fkey;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_variant_id_fkey
    FOREIGN KEY (variant_id) REFERENCES product_variants(id) ON DELETE CASCADE;

-- The cleanup job deletes carts by age, and the storefront reads a cart by its
-- token on every request.
CREATE INDEX ON carts (updated_at);

-- +goose Down
DROP INDEX carts_updated_at_idx;
ALTER TABLE cart_items DROP CONSTRAINT cart_items_variant_id_fkey;
ALTER TABLE cart_items ADD CONSTRAINT cart_items_variant_id_fkey
    FOREIGN KEY (variant_id) REFERENCES product_variants(id);
