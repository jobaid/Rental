DROP TABLE IF EXISTS stripe_events;
DROP INDEX IF EXISTS payments_stripe_session_uq;
ALTER TABLE payments
    DROP COLUMN IF EXISTS invoice_id,
    DROP COLUMN IF EXISTS customer_id,
    DROP COLUMN IF EXISTS stripe_session_id,
    DROP COLUMN IF EXISTS stripe_payment_intent,
    DROP COLUMN IF EXISTS stripe_mode;
