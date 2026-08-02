-- +goose Up
CREATE TABLE products (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL,                    -- free-form, e.g. 'book' | 'apparel'
    slug        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    image_url   TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The variant is the purchasable, priced, stocked unit. A single-edition book
-- still gets exactly one row (size='', color=''), so cart/order/stock logic
-- never branches on "has options vs not" — one code path everywhere.
CREATE TABLE product_variants (
    id          UUID PRIMARY KEY,
    product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    sku         TEXT NOT NULL UNIQUE,
    size        TEXT NOT NULL DEFAULT '',
    color       TEXT NOT NULL DEFAULT '',
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
    stock_qty   INTEGER NOT NULL DEFAULT 0 CHECK (stock_qty >= 0),
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (product_id, size, color)
);
CREATE INDEX ON product_variants (product_id);

-- Anonymous carts, keyed by an opaque random token that is also the cookie value.
CREATE TABLE carts (
    id         TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE cart_items (
    id         BIGSERIAL PRIMARY KEY,
    cart_id    TEXT NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    variant_id UUID NOT NULL REFERENCES product_variants(id),
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    UNIQUE (cart_id, variant_id)
);

CREATE TABLE orders (
    id                UUID PRIMARY KEY,           -- sent to the gateway as its reference
    cart_id           TEXT REFERENCES carts(id) ON DELETE SET NULL,
    customer_name     TEXT NOT NULL,
    customer_email    TEXT NOT NULL,
    customer_phone    TEXT NOT NULL DEFAULT '',
    shipping_address  TEXT NOT NULL DEFAULT '',
    total_cents       BIGINT NOT NULL,
    currency          TEXT NOT NULL,              -- from config; ZAR for PayFast
    status            TEXT NOT NULL DEFAULT 'pending',  -- pending|paid|failed|cancelled
    -- gateway-agnostic payment columns, so a second gateway needs no migration
    gateway           TEXT NOT NULL DEFAULT '',   -- 'payfast'
    gateway_ref       TEXT,                       -- e.g. PayFast pf_payment_id
    gateway_status    TEXT NOT NULL DEFAULT '',   -- e.g. 'COMPLETE'
    gateway_amount    TEXT NOT NULL DEFAULT '',   -- as received, for audit
    gateway_payload   TEXT NOT NULL DEFAULT '',   -- raw callback body, for disputes
    emailed           BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at           TIMESTAMPTZ
);
CREATE INDEX ON orders (status);
CREATE UNIQUE INDEX ON orders (gateway, gateway_ref) WHERE gateway_ref IS NOT NULL;

CREATE TABLE order_items (
    id               BIGSERIAL PRIMARY KEY,
    order_id         UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    variant_id       UUID NOT NULL REFERENCES product_variants(id),
    -- snapshots: later catalog edits must never rewrite purchase history
    title            TEXT NOT NULL,
    size             TEXT NOT NULL DEFAULT '',
    color            TEXT NOT NULL DEFAULT '',
    unit_price_cents BIGINT NOT NULL,
    quantity         INTEGER NOT NULL CHECK (quantity > 0)
);
CREATE INDEX ON order_items (order_id);

-- +goose Down
-- Down exists for local development resets only. Production is forward-only:
-- rolling a schema back over live orders loses money, not just columns.
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE product_variants;
DROP TABLE products;
