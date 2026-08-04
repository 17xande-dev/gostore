-- +goose Up
-- oversold records that a paid order could not have its stock decremented, because
-- there was not enough left by the time the payment arrived.
--
-- Stock is taken at payment, not reserved at checkout, so two shoppers can both
-- reach a payment page for the last item and both pay. The second one's order is
-- still recorded paid — the money has been taken, and refusing to record it would
-- lose the sale *and* still be oversold — and this flag is how a human finds out.
--
-- Until now it existed only in the logs and in the owner's notification email,
-- which is the wrong place for something that has to be reconciled: an email is
-- read once and a log is not read at all.
ALTER TABLE orders ADD COLUMN oversold BOOLEAN NOT NULL DEFAULT FALSE;

-- Partial, because the interesting query is "which orders need attention" and
-- almost none of them do.
CREATE INDEX ON orders (created_at DESC) WHERE oversold;

-- +goose Down
ALTER TABLE orders DROP COLUMN oversold;
