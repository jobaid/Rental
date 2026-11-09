-- Stripe online payments: extend the existing payments table + add a
-- webhook-events table for idempotent processing.

ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS invoice_id              UUID REFERENCES invoices(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS customer_id             UUID REFERENCES customers(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS stripe_session_id       TEXT,
    ADD COLUMN IF NOT EXISTS stripe_payment_intent   TEXT,
    ADD COLUMN IF NOT EXISTS stripe_mode             TEXT;    -- 'test' | 'live'

-- Ensure the same Stripe Checkout Session can't produce two payment rows.
CREATE UNIQUE INDEX IF NOT EXISTS payments_stripe_session_uq
    ON payments(stripe_session_id) WHERE stripe_session_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS payments_invoice_idx ON payments(invoice_id);

-- Webhook event log — one row per received event. The unique constraint on
-- provider_event_id makes replayed webhooks a no-op.
CREATE TABLE IF NOT EXISTS stripe_events (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id         UUID REFERENCES companies(id) ON DELETE CASCADE,
    provider_event_id  TEXT NOT NULL,
    event_type         TEXT NOT NULL,
    livemode           BOOLEAN NOT NULL DEFAULT FALSE,
    raw_payload        JSONB,
    processed_at       TIMESTAMPTZ,
    error_message      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider_event_id)
);
CREATE INDEX IF NOT EXISTS stripe_events_type_idx ON stripe_events(event_type);
