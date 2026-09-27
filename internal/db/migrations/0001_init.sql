-- +goose Up
-- The complete greenfield baseline. Also the schema input to sqlc.
-- pg_trgm is database-wide; keeping its objects in public makes them available
-- to the private schemas used by tests as well as the application's schema.
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;

CREATE TABLE admin_users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'manager', 'viewer')),
    disabled      BOOLEAN NOT NULL DEFAULT FALSE,
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);
-- Email identity is case-insensitive; the application also normalises addr-spec.
CREATE UNIQUE INDEX admin_users_email_key ON admin_users (lower(email));

CREATE TABLE products (
    id          UUID PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- Keys, not deployment-specific URLs, work with both disk and R2 storage.
    image_key   TEXT NOT NULL DEFAULT '',
    kind        TEXT NOT NULL DEFAULT 'physical' CHECK (kind IN ('physical', 'digital')),
    option1_name TEXT NOT NULL DEFAULT '',
    option2_name TEXT NOT NULL DEFAULT '',
    option3_name TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    search      tsvector GENERATED ALWAYS AS (
                    setweight(to_tsvector('english', title), 'A') ||
                    setweight(to_tsvector('english', description), 'B')
                ) STORED
);
-- Full-text matches words; trigrams also tolerate misspellings.
CREATE INDEX products_search_idx ON products USING GIN (search);
CREATE INDEX products_title_trgm_idx ON products USING GIN (title gin_trgm_ops);

CREATE TABLE categories (
    id       UUID PRIMARY KEY,
    slug     TEXT NOT NULL UNIQUE,
    name     TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE product_categories (
    product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    category_id UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, category_id)
);
CREATE INDEX ON product_categories (category_id);

-- Every product has a purchasable variant, even when it has no option choices.
CREATE TABLE product_variants (
    id          UUID PRIMARY KEY,
    product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    sku         TEXT NOT NULL UNIQUE,
    option1     TEXT NOT NULL DEFAULT '',
    option2     TEXT NOT NULL DEFAULT '',
    option3     TEXT NOT NULL DEFAULT '',
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
    stock_qty   INTEGER NOT NULL DEFAULT 0 CHECK (stock_qty >= 0),
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    CONSTRAINT product_variants_options_key UNIQUE (product_id, option1, option2, option3)
);
CREATE INDEX ON product_variants (product_id);

CREATE TABLE carts (
    id         TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Checkout snapshots this version so late payments cannot clear newer edits.
    version    BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX ON carts (updated_at);

-- Cart lines are ephemeral; order lines below deliberately restrict deletion
-- of their variant so catalog edits cannot erase purchase history.
CREATE TABLE cart_items (
    id         BIGSERIAL PRIMARY KEY,
    cart_id    TEXT NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    variant_id UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    UNIQUE (cart_id, variant_id)
);

CREATE TABLE orders (
    id                UUID PRIMARY KEY,
    cart_id           TEXT REFERENCES carts(id) ON DELETE SET NULL,
    customer_name     TEXT NOT NULL,
    customer_email    TEXT NOT NULL,
    customer_phone    TEXT NOT NULL DEFAULT '',
    shipping_address  TEXT NOT NULL DEFAULT '',
    total_cents       BIGINT NOT NULL,
    currency          TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    gateway           TEXT NOT NULL DEFAULT '',
    gateway_ref       TEXT,
    gateway_status    TEXT NOT NULL DEFAULT '',
    gateway_amount    TEXT NOT NULL DEFAULT '',
    gateway_payload   TEXT NOT NULL DEFAULT '',
    emailed           BOOLEAN NOT NULL DEFAULT FALSE,
    -- Payment is recorded even when stock has run out; this flags human follow-up.
    oversold          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at           TIMESTAMPTZ,
    cart_version      BIGINT,
    checkout_key      TEXT,
    -- Fulfillment is independent of immutable payment facts.
    fulfilled_at      TIMESTAMPTZ,
    tracking_reference TEXT NOT NULL DEFAULT '',
    internal_note     TEXT NOT NULL DEFAULT '',
    fulfillment_updated_at TIMESTAMPTZ,
    fulfillment_updated_by UUID REFERENCES admin_users(id) ON DELETE SET NULL
);
CREATE INDEX ON orders (status);
CREATE UNIQUE INDEX ON orders (gateway, gateway_ref) WHERE gateway_ref IS NOT NULL;
CREATE INDEX ON orders (created_at DESC) WHERE oversold;
CREATE UNIQUE INDEX orders_checkout_key ON orders (cart_id, checkout_key);
CREATE INDEX orders_cart_created ON orders (cart_id, created_at DESC);
CREATE INDEX orders_created ON orders (created_at DESC, id DESC);

CREATE TABLE order_items (
    id               BIGSERIAL PRIMARY KEY,
    order_id         UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    variant_id       UUID NOT NULL REFERENCES product_variants(id),
    -- Snapshots: prices, titles, options and delivery kind survive catalog edits.
    title            TEXT NOT NULL,
    variant_label    TEXT NOT NULL DEFAULT '',
    kind             TEXT NOT NULL DEFAULT 'physical',
    unit_price_cents BIGINT NOT NULL,
    quantity         INTEGER NOT NULL CHECK (quantity > 0)
);
CREATE INDEX ON order_items (order_id);

-- Files belong to a product; variant_files lets bundles share the same bytes.
-- Object keys here always name private storage, never the public image bucket.
CREATE TABLE product_files (
    id                BIGSERIAL PRIMARY KEY,
    product_id        UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    position          INTEGER NOT NULL DEFAULT 0,
    title             TEXT NOT NULL,
    object_key        TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    content_type      TEXT NOT NULL,
    size_bytes        BIGINT NOT NULL CHECK (size_bytes >= 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON product_files (product_id, position);

CREATE TABLE variant_files (
    variant_id UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
    file_id    BIGINT NOT NULL REFERENCES product_files(id) ON DELETE CASCADE,
    PRIMARY KEY (variant_id, file_id)
);
CREATE INDEX ON variant_files (file_id);

-- One revocable entitlement per purchased digital line. Only the token's hash
-- belongs here; pending email jobs retain its plaintext encrypted until sent.
CREATE TABLE entitlements (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id      UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    order_item_id BIGINT NOT NULL REFERENCES order_items(id) ON DELETE CASCADE,
    variant_id    UUID NOT NULL REFERENCES product_variants(id),
    token_hash    BYTEA NOT NULL UNIQUE,
    revoked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON entitlements (order_id);

-- Records authorisation, not completion of a transfer through a signed URL.
CREATE TABLE download_events (
    id             BIGSERIAL PRIMARY KEY,
    entitlement_id UUID NOT NULL REFERENCES entitlements(id) ON DELETE CASCADE,
    file_id        BIGINT NOT NULL REFERENCES product_files(id) ON DELETE CASCADE,
    ip             TEXT NOT NULL DEFAULT '',
    user_agent     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON download_events (file_id, created_at DESC);
CREATE INDEX ON download_events (entitlement_id);

CREATE TABLE email_jobs (
    id              BIGSERIAL PRIMARY KEY,
    order_id        UUID NOT NULL REFERENCES orders(id),
    kind            TEXT NOT NULL CHECK (kind IN ('confirmation', 'owner')),
    payload         BYTEA NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT NOT NULL DEFAULT '',
    sent_at         TIMESTAMPTZ,
    UNIQUE (order_id, kind)
);
CREATE INDEX email_jobs_pending ON email_jobs (next_attempt_at, id) WHERE sent_at IS NULL;

CREATE TABLE admin_sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX admin_sessions_user_idx ON admin_sessions (user_id);
CREATE INDEX admin_sessions_expires_idx ON admin_sessions (expires_at);

-- Single-use claim of the first administrator, with no default password.
CREATE TABLE admin_setup (
    id          BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    token_hash  BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed_at TIMESTAMPTZ
);

-- +goose Down
-- Local development only. pg_trgm is database-wide and may have other users.
DROP TABLE admin_setup;
DROP TABLE admin_sessions;
DROP TABLE email_jobs;
DROP TABLE download_events;
DROP TABLE entitlements;
DROP TABLE variant_files;
DROP TABLE product_files;
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE product_variants;
DROP TABLE product_categories;
DROP TABLE categories;
DROP TABLE products;
DROP TABLE admin_users;
