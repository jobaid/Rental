-- Core multi-tenant schema (Phase 1).

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Subscription plans offered by the SaaS owner.
CREATE TABLE subscription_plans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    monthly_price_cents INT NOT NULL DEFAULT 0,
    annual_price_cents  INT NOT NULL DEFAULT 0,
    max_users       INT,
    max_vehicles    INT,
    max_branches    INT,
    features        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- A rental company (tenant).
CREATE TABLE companies (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    slug            TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','suspended','trial','cancelled')),
    plan_id         UUID REFERENCES subscription_plans(id),
    billing_cycle   TEXT CHECK (billing_cycle IN ('monthly','annual')),
    trial_ends_at   TIMESTAMPTZ,
    current_period_end TIMESTAMPTZ,
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Users. company_id is NULL for the SaaS platform owner(s); otherwise it's the tenant.
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID REFERENCES companies(id) ON DELETE CASCADE,
    email           TEXT NOT NULL,
    password_hash   TEXT NOT NULL,
    name            TEXT NOT NULL,
    role            TEXT NOT NULL
                      CHECK (role IN (
                        'platform_owner',
                        'company_owner',
                        'rental_manager',
                        'branch_manager',
                        'reservation_agent',
                        'fleet_manager',
                        'technician',
                        'accountant',
                        'auditor',
                        'customer'
                      )),
    email_verified_at TIMESTAMPTZ,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at   TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Email unique per tenant; platform owners share NULL company_id so unique on (NULL,email) still works via partial index below.
    UNIQUE (company_id, email)
);

-- Partial unique index so platform_owner emails (company_id IS NULL) are also unique.
CREATE UNIQUE INDEX users_platform_email_idx
    ON users (email) WHERE company_id IS NULL;

-- Branches / locations within a company.
CREATE TABLE branches (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    address         TEXT,
    city            TEXT,
    country         TEXT,
    timezone        TEXT NOT NULL DEFAULT 'UTC',
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX branches_company_idx ON branches(company_id);

-- Vehicle categories (sedan, SUV, van, etc.) per company.
CREATE TABLE vehicle_categories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, name)
);

-- Vehicles.
CREATE TABLE vehicles (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    branch_id       UUID REFERENCES branches(id),
    category_id     UUID REFERENCES vehicle_categories(id),
    fleet_number    TEXT,
    license_plate   TEXT,
    vin             TEXT,
    make            TEXT,
    model           TEXT,
    year            INT,
    trim            TEXT,
    transmission    TEXT CHECK (transmission IN ('automatic','manual','cvt','other') OR transmission IS NULL),
    fuel_type       TEXT,
    seats           INT,
    color_exterior  TEXT,
    color_interior  TEXT,
    odometer_km     INT,
    purchase_date   DATE,
    purchase_cost_cents BIGINT,
    daily_rate_cents    INT NOT NULL DEFAULT 0,
    weekly_rate_cents   INT,
    monthly_rate_cents  INT,
    deposit_cents       INT NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'available'
                      CHECK (status IN ('available','reserved','rented','maintenance','out_of_service','inactive')),
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, fleet_number),
    UNIQUE (company_id, license_plate)
);
CREATE INDEX vehicles_company_status_idx ON vehicles(company_id, status);
CREATE INDEX vehicles_branch_idx ON vehicles(branch_id);

-- Customers (end renters) per company.
CREATE TABLE customers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id),
    first_name      TEXT NOT NULL,
    last_name       TEXT NOT NULL,
    email           TEXT,
    phone           TEXT,
    address         TEXT,
    license_number  TEXT,
    license_expiry  DATE,
    notes           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX customers_company_idx ON customers(company_id);
CREATE INDEX customers_company_email_idx ON customers(company_id, email);

-- Audit log for sensitive actions.
CREATE TABLE audit_events (
    id              BIGSERIAL PRIMARY KEY,
    company_id      UUID REFERENCES companies(id) ON DELETE SET NULL,
    actor_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    action          TEXT NOT NULL,
    target_type     TEXT,
    target_id       TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX audit_company_idx ON audit_events(company_id, created_at DESC);

-- Seed a starter plan so onboarding has something to attach to.
INSERT INTO subscription_plans (code, name, monthly_price_cents, annual_price_cents, max_users, max_vehicles, max_branches, features)
VALUES
  ('starter',  'Starter',  4900,   49000,  5,  25,  1, '{"maintenance":true,"reports":true}'),
  ('growth',   'Growth',   14900,  149000, 25, 200, 5, '{"maintenance":true,"reports":true,"channels":true}'),
  ('enterprise','Enterprise',49900, 499000, NULL, NULL, NULL, '{"maintenance":true,"reports":true,"channels":true,"api":true}');
