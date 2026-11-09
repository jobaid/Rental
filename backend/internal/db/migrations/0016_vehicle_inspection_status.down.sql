UPDATE vehicles SET status='maintenance' WHERE status='inspection_pending';
ALTER TABLE vehicles DROP CONSTRAINT IF EXISTS vehicles_status_check;
ALTER TABLE vehicles
    ADD CONSTRAINT vehicles_status_check
    CHECK (status IN ('available','reserved','rented','maintenance','out_of_service','inactive'));
