-- +goose Up
-- The shared outbox module owns the queue now, in its own tables and history.
-- Development databases lose their queued jobs: the module uses a new schema and
-- a new seal format, and nothing has been deployed with the old rows.
DROP TABLE email_jobs;

-- +goose Down
-- The old schema is recreated without its former rows.
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
