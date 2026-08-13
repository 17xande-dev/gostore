-- +goose Up

-- Trigram matching for the catalog search. In `public` explicitly, not into
-- whatever search_path happens to lead with: extension objects are schema-scoped
-- but extension *names* are database-global, so a bare CREATE EXTENSION lands in
-- the first schema to run it and is invisible from every other one. That failure
-- is order-dependent, which makes it a test suite that passes one at a time and
-- fails together.
CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;

CREATE TABLE products (
    id          UUID PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- image_key names the object in blob storage that this product's image *is*.
    -- The URL is not stored: it is this key resolved against whichever backend is
    -- configured, at render time. Storing it as well would bake one deployment's
    -- answer into every row, so moving a store from a local directory to a bucket —
    -- or putting a custom domain in front of one — would need an UPDATE across the
    -- table before any image loaded again.
    image_key   TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Generated rather than maintained by a trigger, so it cannot fall out of step
    -- with the row. to_tsvector's two-argument form is load-bearing: with a literal
    -- config it is immutable, which is what makes GENERATED ... STORED legal at all.
    -- setweight puts a title match above the same word buried in a description.
    search      tsvector GENERATED ALWAYS AS (
                    setweight(to_tsvector('english', title), 'A') ||
                    setweight(to_tsvector('english', description), 'B')
                ) STORED
);
-- Full-text finds words, trigram finds spellings: websearch_to_tsquery stems, so
-- "running" reaches "run", but it dies on a typo; trigram survives the typo and has
-- no idea the two words are related. Each covers the other's blind spot, which is
-- why both indexes are here rather than one.
CREATE INDEX products_search_idx     ON products USING GIN (search);
CREATE INDEX products_title_trgm_idx ON products USING GIN (title gin_trgm_ops);

-- A category is a row, not a string on the product. slug is the public URL
-- parameter; position is the display order, because ordering by name would put
-- "Apparel" ahead of "Books" for ever and a shop owner wants their own order.
CREATE TABLE categories (
    id       UUID PRIMARY KEY,
    slug     TEXT NOT NULL UNIQUE,
    name     TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);

-- A join table rather than a column on products: a book that is also a gift belongs
-- in both, and forcing that choice on a shop owner is a decision the store should
-- not make for them. Cascades unlink a deleted category from its products; it never
-- deletes the products themselves.
CREATE TABLE product_categories (
    product_id  UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    category_id UUID NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, category_id)
);
CREATE INDEX ON product_categories (category_id);

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
-- The cleanup job deletes carts by age.
CREATE INDEX ON carts (updated_at);

-- The variant reference cascades here and deliberately restricts in order_items.
-- The two tables look alike and want opposite behaviour: a cart is ephemeral, so a
-- variant sitting in somebody's abandoned cart must not stop the shop owner from
-- deleting it. An order is a record of what was actually bought, and deleting a
-- variant must never quietly rewrite purchase history.
CREATE TABLE cart_items (
    id         BIGSERIAL PRIMARY KEY,
    cart_id    TEXT NOT NULL REFERENCES carts(id) ON DELETE CASCADE,
    variant_id UUID NOT NULL REFERENCES product_variants(id) ON DELETE CASCADE,
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
    -- A paid order whose stock could not be decremented, because there was not
    -- enough left by the time the payment arrived. Stock is taken at payment, not
    -- reserved at checkout, so two shoppers can both reach a payment page for the
    -- last item and both pay. The second order is still recorded paid — the money has
    -- been taken, and refusing to record it would lose the sale *and* still be
    -- oversold — and this flag is how a human finds out. It has to live in the
    -- schema, not only in a log and a notification email: an email is read once and a
    -- log is not read at all.
    oversold          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    paid_at           TIMESTAMPTZ
);
CREATE INDEX ON orders (status);
CREATE UNIQUE INDEX ON orders (gateway, gateway_ref) WHERE gateway_ref IS NOT NULL;
-- Partial, because the only interesting query is "which orders need attention" and
-- almost none of them do.
CREATE INDEX ON orders (created_at DESC) WHERE oversold;

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
--
-- pg_trgm is deliberately not dropped. An extension is database-wide and may have
-- other users, so tearing one schema down must not remove it from under them.
DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE product_variants;
DROP TABLE product_categories;
DROP TABLE categories;
DROP TABLE products;
