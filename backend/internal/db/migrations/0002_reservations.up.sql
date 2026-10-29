-- Phase 4: reservations, availability, blocked periods.
-- Phase 5: customer-facing portal (uses the same tables + the existing 'customer' role).

CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE SEQUENCE IF NOT EXISTS reservations_booking_seq START 100000;

CREATE TABLE reservations (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id       UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    booking_number   TEXT NOT NULL DEFAULT ('BK-' || LPAD(nextval('reservations_booking_seq')::text, 6, '0')),
    customer_id      UUID REFERENCES customers(id),
    vehicle_id       UUID REFERENCES vehicles(id),
    pickup_branch_id UUID REFERENCES branches(id),
    return_branch_id UUID REFERENCES branches(id),
    pickup_at        TIMESTAMPTZ NOT NULL,
    return_at        TIMESTAMPTZ NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('inquiry','pending','confirmed','checked_out','returned','cancelled','no_show')),
    daily_rate_cents INT NOT NULL DEFAULT 0,
    rental_days      INT NOT NULL DEFAULT 1,
    subtotal_cents   INT NOT NULL DEFAULT 0,
    extras_cents     INT NOT NULL DEFAULT 0,
    tax_cents        INT NOT NULL DEFAULT 0,
    deposit_cents    INT NOT NULL DEFAULT 0,
    total_cents      INT NOT NULL DEFAULT 0,
    paid_cents       INT NOT NULL DEFAULT 0,
    notes            TEXT,
    source           TEXT NOT NULL DEFAULT 'manager'
                       CHECK (source IN ('manager','customer','walkin','channel')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (return_at > pickup_at),
    UNIQUE (company_id, booking_number)
);

CREATE INDEX reservations_company_dates_idx ON reservations(company_id, pickup_at, return_at);
CREATE INDEX reservations_vehicle_dates_idx ON reservations(vehicle_id, pickup_at, return_at);
CREATE INDEX reservations_status_idx ON reservations(company_id, status);

-- Prevent overlapping active reservations on the same vehicle.
-- The partial WHERE clause lets cancelled / no_show / returned bookings coexist with new ones.
ALTER TABLE reservations ADD CONSTRAINT reservations_no_overlap
  EXCLUDE USING gist (
    vehicle_id WITH =,
    tstzrange(pickup_at, return_at, '[)') WITH &&
  ) WHERE (vehicle_id IS NOT NULL AND status IN ('pending','confirmed','checked_out'));

-- Maintenance / out-of-service blocks on a vehicle.
CREATE TABLE blocked_periods (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id  UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id  UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL,
    reason      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX blocked_vehicle_dates_idx ON blocked_periods(vehicle_id, starts_at, ends_at);

ALTER TABLE blocked_periods ADD CONSTRAINT blocked_no_overlap
  EXCLUDE USING gist (
    vehicle_id WITH =,
    tstzrange(starts_at, ends_at, '[)') WITH &&
  );

-- Optional link so a 'customer' user can see their own reservations in the portal.
-- customers.user_id already exists from migration 0001.

-- Trigger to keep updated_at fresh on reservations.
CREATE OR REPLACE FUNCTION touch_updated_at() RETURNS trigger AS $$
BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER reservations_touch BEFORE UPDATE ON reservations
  FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
