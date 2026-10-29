-- Phase 2-7 expansion: photos, mileage, inspections, documents, settings, custom fields, oil changes, status history.

-- Expand vehicles with spec fields.
ALTER TABLE vehicles
  ADD COLUMN IF NOT EXISTS luggage_capacity INT,
  ADD COLUMN IF NOT EXISTS doors INT,
  ADD COLUMN IF NOT EXISTS engine TEXT,
  ADD COLUMN IF NOT EXISTS features JSONB NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS hourly_rate_cents INT,
  ADD COLUMN IF NOT EXISTS included_km_per_day INT,
  ADD COLUMN IF NOT EXISTS extra_km_cents INT,
  ADD COLUMN IF NOT EXISTS fuel_policy TEXT,
  ADD COLUMN IF NOT EXISTS protection_options JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS registration_expiry DATE,
  ADD COLUMN IF NOT EXISTS insurance_expiry DATE,
  ADD COLUMN IF NOT EXISTS inspection_expiry DATE,
  ADD COLUMN IF NOT EXISTS oil_type TEXT,
  ADD COLUMN IF NOT EXISTS oil_viscosity TEXT,
  ADD COLUMN IF NOT EXISTS oil_filter TEXT,
  ADD COLUMN IF NOT EXISTS next_oil_change_date DATE,
  ADD COLUMN IF NOT EXISTS next_oil_change_km INT;

-- Broaden status check (drop then recreate).
ALTER TABLE vehicles DROP CONSTRAINT IF EXISTS vehicles_status_check;
ALTER TABLE vehicles ADD CONSTRAINT vehicles_status_check CHECK (status IN (
    'available','reserved','awaiting_pickup','rented','return_overdue',
    'inspection_pending','cleaning','inspection_required','maintenance_required',
    'maintenance','out_of_service','inactive','retired'
));

-- Vehicle photographs.
CREATE TABLE IF NOT EXISTS vehicle_photos (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    kind            TEXT,
    caption         TEXT,
    file_path       TEXT NOT NULL,
    is_primary      BOOLEAN NOT NULL DEFAULT FALSE,
    display_order   INT NOT NULL DEFAULT 0,
    uploaded_by     UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS vehicle_photos_vehicle_idx ON vehicle_photos(vehicle_id, display_order);

-- Vehicle documents (registration, insurance, inspection cert).
CREATE TABLE IF NOT EXISTS vehicle_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    expires_on      DATE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Customer documents (license, ID, proof of address).
CREATE TABLE IF NOT EXISTS customer_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    file_path       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','verified','rejected')),
    verified_by     UUID REFERENCES users(id),
    verified_at     TIMESTAMPTZ,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS customer_documents_customer_idx ON customer_documents(customer_id);

-- Mileage tracking.
CREATE TABLE IF NOT EXISTS mileage_records (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    reservation_id  UUID REFERENCES reservations(id) ON DELETE SET NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('out','in','correction')),
    odometer_km     INT NOT NULL,
    fuel_level_pct  INT,
    photo_path      TEXT,
    recorded_by     UUID REFERENCES users(id),
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS mileage_vehicle_idx ON mileage_records(vehicle_id, created_at DESC);
CREATE INDEX IF NOT EXISTS mileage_reservation_idx ON mileage_records(reservation_id);

-- Inspection photos (ties to the existing inspections table from 0003).
CREATE TABLE IF NOT EXISTS inspection_photos (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inspection_id   UUID NOT NULL REFERENCES inspections(id) ON DELETE CASCADE,
    kind            TEXT,
    caption         TEXT,
    file_path       TEXT NOT NULL,
    display_order   INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS inspection_photos_idx ON inspection_photos(inspection_id);

-- Oil-change log (separate from generic maintenance_records so reminders are easy).
CREATE TABLE IF NOT EXISTS oil_changes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    performed_on    DATE NOT NULL DEFAULT CURRENT_DATE,
    odometer_km     INT,
    oil_type        TEXT,
    oil_viscosity   TEXT,
    oil_filter      TEXT,
    next_due_date   DATE,
    next_due_km     INT,
    vendor          TEXT,
    cost_cents      INT NOT NULL DEFAULT 0,
    notes           TEXT,
    receipt_path    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS oil_changes_vehicle_idx ON oil_changes(vehicle_id, performed_on DESC);

-- Reservation status history + extensions.
CREATE TABLE IF NOT EXISTS reservation_status_history (
    id              BIGSERIAL PRIMARY KEY,
    reservation_id  UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    from_status     TEXT,
    to_status       TEXT NOT NULL,
    changed_by      UUID REFERENCES users(id),
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS res_hist_idx ON reservation_status_history(reservation_id, created_at DESC);

-- Settings — a per-company key/value store.
CREATE TABLE IF NOT EXISTS settings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    key             TEXT NOT NULL,
    value           JSONB NOT NULL,
    updated_by      UUID REFERENCES users(id),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, key)
);

-- Custom field builder.
CREATE TABLE IF NOT EXISTS custom_fields (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    entity          TEXT NOT NULL CHECK (entity IN ('vehicle','customer','reservation','inspection','maintenance')),
    name            TEXT NOT NULL,
    label           TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('text','longtext','number','currency','date','datetime','select','multiselect','checkbox','email','phone','file')),
    options         JSONB,
    required        BOOLEAN NOT NULL DEFAULT FALSE,
    display_order   INT NOT NULL DEFAULT 0,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, entity, name)
);

CREATE TABLE IF NOT EXISTS custom_field_values (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    field_id        UUID NOT NULL REFERENCES custom_fields(id) ON DELETE CASCADE,
    entity_id       UUID NOT NULL,
    value           JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (field_id, entity_id)
);
CREATE INDEX IF NOT EXISTS cfv_entity_idx ON custom_field_values(entity_id);

-- Promo codes.
CREATE TABLE IF NOT EXISTS promo_codes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    code            TEXT NOT NULL,
    percent_off     INT,
    amount_off_cents INT,
    valid_from      DATE,
    valid_until     DATE,
    max_uses        INT,
    uses            INT NOT NULL DEFAULT 0,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, code)
);

-- Support tickets.
CREATE TABLE IF NOT EXISTS support_tickets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id     UUID REFERENCES customers(id),
    subject         TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','resolved','closed')),
    priority        TEXT NOT NULL DEFAULT 'normal',
    assigned_to     UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS support_messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ticket_id       UUID NOT NULL REFERENCES support_tickets(id) ON DELETE CASCADE,
    author_user_id  UUID REFERENCES users(id),
    is_internal     BOOLEAN NOT NULL DEFAULT FALSE,
    body            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Backups log.
CREATE TABLE IF NOT EXISTS backup_runs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID REFERENCES companies(id) ON DELETE CASCADE,
    kind            TEXT NOT NULL,
    status          TEXT NOT NULL,
    file_path       TEXT,
    size_bytes      BIGINT,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at     TIMESTAMPTZ,
    note            TEXT
);

-- Trigger: write a status-history row whenever reservations.status changes.
CREATE OR REPLACE FUNCTION reservations_status_log() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO reservation_status_history (reservation_id, from_status, to_status)
        VALUES (NEW.id, NULL, NEW.status);
    ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
        INSERT INTO reservation_status_history (reservation_id, from_status, to_status)
        VALUES (NEW.id, OLD.status, NEW.status);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS reservations_status_log_trg ON reservations;
CREATE TRIGGER reservations_status_log_trg
AFTER INSERT OR UPDATE ON reservations
FOR EACH ROW EXECUTE FUNCTION reservations_status_log();

-- Seed default settings namespace for existing companies (null-safe JSON).
INSERT INTO settings (company_id, key, value)
SELECT c.id, 'general', jsonb_build_object('currency','USD','timezone','UTC','mileageUnit','km','businessName',c.name)
FROM companies c
ON CONFLICT (company_id, key) DO NOTHING;

INSERT INTO settings (company_id, key, value)
SELECT c.id, 'rental', jsonb_build_object('minHours',24,'maxDays',90,'graceMinutes',60,'depositDefaultCents',20000)
FROM companies c
ON CONFLICT (company_id, key) DO NOTHING;
