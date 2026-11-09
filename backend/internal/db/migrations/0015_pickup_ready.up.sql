-- "Ready for pickup" signal: after the fleet team finishes the pre-rental
-- inspection, the manager clicks Ready and the customer sees a clear
-- "your car is ready" state in the portal.
ALTER TABLE reservations
    ADD COLUMN IF NOT EXISTS ready_for_pickup_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS ready_for_pickup_by   UUID REFERENCES users(id),
    ADD COLUMN IF NOT EXISTS ready_for_pickup_note TEXT;
