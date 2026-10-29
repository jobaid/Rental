DROP TRIGGER IF EXISTS reservations_touch ON reservations;
DROP FUNCTION IF EXISTS touch_updated_at();
DROP TABLE IF EXISTS blocked_periods;
DROP TABLE IF EXISTS reservations;
DROP SEQUENCE IF EXISTS reservations_booking_seq;
