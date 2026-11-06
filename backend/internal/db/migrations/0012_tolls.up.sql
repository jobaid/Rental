-- Toll management: E-ZPass Virginia (and compatible agency) transponders,
-- transaction imports, and matching to rental reservations.

-- A single row per connected toll account (one per company for now).
CREATE TABLE IF NOT EXISTS toll_accounts (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id         UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    provider           TEXT NOT NULL DEFAULT 'ezpass_va',    -- ezpass_va | ezpass_md | verra | bestpass | other
    account_reference  TEXT,                                 -- external account number (optional)
    import_method      TEXT NOT NULL DEFAULT 'csv'           -- csv | api (future)
                         CHECK (import_method IN ('csv','api')),
    connection_status  TEXT NOT NULL DEFAULT 'disconnected'
                         CHECK (connection_status IN ('disconnected','ready','error')),
    last_sync_at       TIMESTAMPTZ,
    last_sync_error    TEXT,
    admin_fee_cents    INT  NOT NULL DEFAULT 500,            -- default per-rental admin fee
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id)                                      -- single toll account per company for now
);

-- One row per physical transponder/device. Identifier is the tag number
-- printed on the device.
CREATE TABLE IF NOT EXISTS toll_transponders (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id         UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    toll_account_id    UUID REFERENCES toll_accounts(id) ON DELETE SET NULL,
    transponder_id     TEXT NOT NULL,                       -- tag number (eg. "001234567")
    serial_number      TEXT,
    status             TEXT NOT NULL DEFAULT 'active'
                         CHECK (status IN ('active','inactive','lost','damaged','under_replacement')),
    notes              TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, transponder_id)
);
CREATE INDEX IF NOT EXISTS toll_transponders_company_idx ON toll_transponders(company_id);

-- Assignment history — one row per (transponder <-> vehicle) span. Preserves
-- who had which tag at any past moment so a toll from six months ago still
-- matches the vehicle that had the device then.
CREATE TABLE IF NOT EXISTS vehicle_transponder_assignments (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id         UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    transponder_id     UUID NOT NULL REFERENCES toll_transponders(id) ON DELETE CASCADE,
    vehicle_id         UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    assigned_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    removed_at         TIMESTAMPTZ,                          -- NULL = still current
    created_by         UUID REFERENCES users(id),
    note               TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS vta_transponder_idx ON vehicle_transponder_assignments(transponder_id, assigned_at);
CREATE INDEX IF NOT EXISTS vta_vehicle_idx     ON vehicle_transponder_assignments(vehicle_id, assigned_at);
-- Only one "current" (removed_at NULL) assignment per transponder.
CREATE UNIQUE INDEX IF NOT EXISTS vta_one_active_per_transponder
    ON vehicle_transponder_assignments(transponder_id)
    WHERE removed_at IS NULL;

-- Imported toll transactions, one per provider event. Keeps the raw
-- (provider_transaction_id, provider) pair unique so re-imports are safe.
CREATE TABLE IF NOT EXISTS toll_transactions (
    id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id             UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    provider               TEXT NOT NULL,                   -- ezpass_va | ...
    provider_transaction_id TEXT NOT NULL,                  -- unique in the provider file
    transponder_ref        TEXT,                            -- as reported by provider
    transponder_id         UUID REFERENCES toll_transponders(id) ON DELETE SET NULL,
    vehicle_id             UUID REFERENCES vehicles(id) ON DELETE SET NULL,
    reservation_id         UUID REFERENCES reservations(id) ON DELETE SET NULL,
    customer_id            UUID REFERENCES customers(id) ON DELETE SET NULL,
    license_plate          TEXT,
    toll_location          TEXT,                            -- plaza / facility
    entry_plaza            TEXT,                            -- VA Express Lanes: entry gantry
    exit_plaza             TEXT,                            -- VA Express Lanes: exit gantry
    transaction_at         TIMESTAMPTZ NOT NULL,
    amount_cents           INT NOT NULL DEFAULT 0,
    admin_fee_cents        INT NOT NULL DEFAULT 0,
    currency               TEXT NOT NULL DEFAULT 'USD',
    match_status           TEXT NOT NULL DEFAULT 'pending'
                             CHECK (match_status IN ('pending','matched','company_expense','needs_review','excluded')),
    billing_status         TEXT NOT NULL DEFAULT 'unbilled'
                             CHECK (billing_status IN ('unbilled','invoiced','paid','disputed','waived')),
    exclusion_reason       TEXT,
    verified_at            TIMESTAMPTZ,
    verified_by            UUID REFERENCES users(id),
    raw_row                JSONB,                           -- original CSV row for audit
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, provider, provider_transaction_id)
);
CREATE INDEX IF NOT EXISTS tt_transponder_idx ON toll_transactions(transponder_id);
CREATE INDEX IF NOT EXISTS tt_vehicle_idx     ON toll_transactions(vehicle_id);
CREATE INDEX IF NOT EXISTS tt_reservation_idx ON toll_transactions(reservation_id);
CREATE INDEX IF NOT EXISTS tt_customer_idx    ON toll_transactions(customer_id);
CREATE INDEX IF NOT EXISTS tt_when_idx        ON toll_transactions(transaction_at DESC);
CREATE INDEX IF NOT EXISTS tt_match_status_idx ON toll_transactions(company_id, match_status);
CREATE INDEX IF NOT EXISTS tt_billing_status_idx ON toll_transactions(company_id, billing_status);

-- Lightweight import-run log so managers can see what came in on what day.
CREATE TABLE IF NOT EXISTS toll_imports (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id         UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    provider           TEXT NOT NULL,
    source_filename    TEXT,
    rows_total         INT  NOT NULL DEFAULT 0,
    rows_inserted      INT  NOT NULL DEFAULT 0,
    rows_duplicate     INT  NOT NULL DEFAULT 0,
    rows_error         INT  NOT NULL DEFAULT 0,
    error_detail       TEXT,
    uploaded_by        UUID REFERENCES users(id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS toll_imports_company_idx ON toll_imports(company_id, created_at DESC);
