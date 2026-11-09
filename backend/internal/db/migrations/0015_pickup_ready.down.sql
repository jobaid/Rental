ALTER TABLE reservations
    DROP COLUMN IF EXISTS ready_for_pickup_at,
    DROP COLUMN IF EXISTS ready_for_pickup_by,
    DROP COLUMN IF EXISTS ready_for_pickup_note;
