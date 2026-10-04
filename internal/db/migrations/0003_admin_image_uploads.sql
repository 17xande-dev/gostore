-- +goose Up
-- Image upload URLs: what the MCP endpoint hands a client so it can send a
-- product image over plain HTTP instead of inside a tool call. Each is a
-- single-use capability for one product, minutes long, issued against the API
-- token that asked for it.
--
-- Shaped like the other credentials: only sha256(token) is stored, and expiry
-- is enforced in the lookup's predicate. Hanging off the API token is what makes
-- it revocable with everything else — revoking the token, or the password, role
-- or disable change that deletes an account's tokens, deletes these with it —
-- and hanging off the product means a deleted product leaves no URL behind.
CREATE TABLE admin_image_uploads (
    token_hash   BYTEA PRIMARY KEY,
    api_token_id UUID NOT NULL REFERENCES admin_api_tokens(id) ON DELETE CASCADE,
    product_id   UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX admin_image_uploads_api_token_idx ON admin_image_uploads (api_token_id);
CREATE INDEX admin_image_uploads_product_idx ON admin_image_uploads (product_id);

-- +goose Down
DROP TABLE admin_image_uploads;
