-- Phase 4-7: booking lifecycle, payments, contracts, maintenance (minimal schema).

CREATE TABLE IF NOT EXISTS reservation_extras (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    reservation_id  UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    price_cents     INT NOT NULL DEFAULT 0,
    quantity        INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS extras_reservation_idx ON reservation_extras(reservation_id);

CREATE TABLE IF NOT EXISTS payments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    reservation_id  UUID REFERENCES reservations(id) ON DELETE SET NULL,
    amount_cents    INT NOT NULL,
    currency        TEXT NOT NULL DEFAULT 'USD',
    method          TEXT NOT NULL DEFAULT 'manual'
                      CHECK (method IN ('manual','card','cash','bank','stripe')),
    status          TEXT NOT NULL DEFAULT 'succeeded'
                      CHECK (status IN ('pending','succeeded','failed','refunded')),
    reference       TEXT,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS payments_company_idx ON payments(company_id, created_at DESC);
CREATE INDEX IF NOT EXISTS payments_reservation_idx ON payments(reservation_id);

CREATE TABLE IF NOT EXISTS rental_contracts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    reservation_id  UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    version         INT NOT NULL DEFAULT 1,
    body            TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('draft','sent','signed','void')),
    signer_name     TEXT,
    signer_email    TEXT,
    signed_at       TIMESTAMPTZ,
    signature_data  TEXT,
    sign_token      TEXT UNIQUE,
    sign_token_exp  TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS contracts_reservation_idx ON rental_contracts(reservation_id);

CREATE TABLE IF NOT EXISTS maintenance_records (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL DEFAULT 'service',
    description     TEXT,
    cost_cents      INT NOT NULL DEFAULT 0,
    odometer_km     INT,
    performed_on    DATE NOT NULL DEFAULT CURRENT_DATE,
    vendor          TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS maint_vehicle_idx ON maintenance_records(vehicle_id, performed_on DESC);

CREATE TABLE IF NOT EXISTS inspections (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    reservation_id  UUID REFERENCES reservations(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL CHECK (kind IN ('pre','post')),
    fuel_level_pct  INT,
    odometer_km     INT,
    damage_notes    TEXT,
    checklist       JSONB NOT NULL DEFAULT '{}'::jsonb,
    inspected_by    UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS inspections_reservation_idx ON inspections(reservation_id);

-- Extend customers with a few useful fields that the UI uses.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS city TEXT;
ALTER TABLE customers ADD COLUMN IF NOT EXISTS country TEXT;

-- Notifications log (so we can see what was sent).
CREATE TABLE IF NOT EXISTS notifications (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID REFERENCES companies(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    recipient       TEXT NOT NULL,
    subject         TEXT,
    body            TEXT,
    status          TEXT NOT NULL DEFAULT 'queued'
                      CHECK (status IN ('queued','sent','failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS notifications_company_idx ON notifications(company_id, created_at DESC);
