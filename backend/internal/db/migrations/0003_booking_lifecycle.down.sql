DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS inspections;
DROP TABLE IF EXISTS maintenance_records;
DROP TABLE IF EXISTS rental_contracts;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS reservation_extras;
ALTER TABLE customers DROP COLUMN IF EXISTS city;
ALTER TABLE customers DROP COLUMN IF EXISTS country;
