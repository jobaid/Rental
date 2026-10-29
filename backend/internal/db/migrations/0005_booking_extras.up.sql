-- Photo-upload tokens: when a booking date approaches the manager emails the customer a link
-- (/upload/:token) to drop pickup/return photos without logging in.
CREATE TABLE IF NOT EXISTS photo_upload_tokens (
    token          TEXT PRIMARY KEY,
    company_id     UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    reservation_id UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('before','after','both')),
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ,
    created_by     UUID REFERENCES users(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS put_reservation_idx ON photo_upload_tokens(reservation_id);

-- Reservation addons catalog: per-company list of optional extras.
CREATE TABLE IF NOT EXISTS addon_catalog (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    code          TEXT NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT,
    price_cents   INT NOT NULL DEFAULT 0,
    price_per     TEXT NOT NULL DEFAULT 'rental' CHECK (price_per IN ('rental','day')),
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    display_order INT NOT NULL DEFAULT 0,
    UNIQUE (company_id, code)
);

-- Insurance options catalog (customer may decline).
CREATE TABLE IF NOT EXISTS insurance_catalog (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id    UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    code          TEXT NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT,
    price_per_day_cents INT NOT NULL DEFAULT 0,
    deductible_cents    INT NOT NULL DEFAULT 0,
    is_default    BOOLEAN NOT NULL DEFAULT FALSE,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    UNIQUE (company_id, code)
);

-- Driver's license verification record.
CREATE TABLE IF NOT EXISTS license_verifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id     UUID REFERENCES customers(id) ON DELETE CASCADE,
    reservation_id  UUID REFERENCES reservations(id) ON DELETE CASCADE,
    license_number  TEXT NOT NULL,
    issuing_country TEXT,
    issuing_state   TEXT,
    expires_on      DATE,
    front_photo     TEXT,
    back_photo      TEXT,
    checksum_valid  BOOLEAN,
    manual_status   TEXT NOT NULL DEFAULT 'pending'
                      CHECK (manual_status IN ('pending','approved','rejected')),
    automated_note  TEXT,
    reviewed_by     UUID REFERENCES users(id),
    reviewed_at     TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS lic_reservation_idx ON license_verifications(reservation_id);
CREATE INDEX IF NOT EXISTS lic_customer_idx    ON license_verifications(customer_id);

-- Reservation now tracks declared insurance choice (nullable; null = declined).
ALTER TABLE reservations
  ADD COLUMN IF NOT EXISTS insurance_code TEXT,
  ADD COLUMN IF NOT EXISTS insurance_total_cents INT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS checkin_odometer_km INT,
  ADD COLUMN IF NOT EXISTS checkin_fuel_pct    INT,
  ADD COLUMN IF NOT EXISTS checkin_at          TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS checkout_odometer_km INT,
  ADD COLUMN IF NOT EXISTS checkout_fuel_pct    INT,
  ADD COLUMN IF NOT EXISTS checkout_at          TIMESTAMPTZ;

-- Seed a basic catalog for every existing company so the booking engine shows something right away.
INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
SELECT c.id, 'child_seat', 'Child seat', 'Rear-facing child seat', 500, 'day', 1 FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
SELECT c.id, 'booster', 'Booster seat', 'For older kids', 400, 'day', 2 FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
SELECT c.id, 'gps', 'GPS unit', 'Portable GPS navigation', 300, 'day', 3 FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
SELECT c.id, 'extra_driver', 'Additional driver', 'Add a second authorized driver', 1500, 'rental', 4 FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
SELECT c.id, 'wifi', 'In-car Wi-Fi', 'Portable 4G hotspot', 700, 'day', 5 FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;

INSERT INTO insurance_catalog (company_id, code, name, description, price_per_day_cents, deductible_cents, is_default)
SELECT c.id, 'none', 'Decline — I have my own coverage', 'I confirm I have valid personal insurance for this rental.', 0, 0, FALSE FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO insurance_catalog (company_id, code, name, description, price_per_day_cents, deductible_cents, is_default)
SELECT c.id, 'basic', 'Basic protection', 'Covers collision damage with a $1,000 deductible.', 1500, 100000, TRUE FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
INSERT INTO insurance_catalog (company_id, code, name, description, price_per_day_cents, deductible_cents, is_default)
SELECT c.id, 'premium', 'Premium zero-deductible', 'Full damage waiver with no deductible.', 2900, 0, FALSE FROM companies c
ON CONFLICT (company_id, code) DO NOTHING;
