ALTER TABLE reservations
    DROP COLUMN IF EXISTS security_deposit_cents,
    DROP COLUMN IF EXISTS deposit_charged_at,
    DROP COLUMN IF EXISTS deposit_refunded_at;
