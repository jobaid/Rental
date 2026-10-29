-- Vehicle rate periods: override the vehicle's default daily rate for a date range.
-- A reservation picks the period that covers its pickup date.

CREATE TABLE IF NOT EXISTS vehicle_rate_periods (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    starts_on       DATE NOT NULL,
    ends_on         DATE NOT NULL,
    daily_rate_cents INT NOT NULL,
    label           TEXT,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (ends_on >= starts_on)
);
CREATE INDEX IF NOT EXISTS vrp_vehicle_dates_idx ON vehicle_rate_periods(vehicle_id, starts_on, ends_on);

-- Prevent overlapping ACTIVE periods for the same vehicle.
ALTER TABLE vehicle_rate_periods ADD CONSTRAINT vrp_no_overlap
  EXCLUDE USING gist (
    vehicle_id WITH =,
    daterange(starts_on, ends_on, '[]') WITH &&
  ) WHERE (is_active);

-- Seed default tax setting for each existing company (10% off, label "Tax").
INSERT INTO settings (company_id, key, value)
SELECT c.id, 'tax', jsonb_build_object('label','Tax','ratePct',0,'inclusive',false)
FROM companies c
ON CONFLICT (company_id, key) DO NOTHING;
