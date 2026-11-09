-- Add 'inspection_pending' to the vehicles.status CHECK constraint so
-- post-return vehicles can sit in a "wash + inspect" state until a manager
-- flips them back to 'available' via the Ready button.
ALTER TABLE vehicles DROP CONSTRAINT IF EXISTS vehicles_status_check;
ALTER TABLE vehicles
    ADD CONSTRAINT vehicles_status_check
    CHECK (status IN ('available','reserved','rented','maintenance','out_of_service','inactive','inspection_pending'));
