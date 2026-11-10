-- Per-reservation security deposit so each booking can carry its own amount
-- (default pulled from vehicle.deposit_cents or settings.rental.depositDefaultCents
-- at booking creation; admin can override on the reservation page).
ALTER TABLE reservations
    ADD COLUMN IF NOT EXISTS security_deposit_cents INT NOT NULL DEFAULT 20000,
    ADD COLUMN IF NOT EXISTS deposit_charged_at     TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deposit_refunded_at    TIMESTAMPTZ;
