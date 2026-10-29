-- Which addon_catalog items apply to which vehicle. When this table has no rows
-- for a vehicle, we fall back to the full catalog (unchanged behavior).
CREATE TABLE IF NOT EXISTS vehicle_addons (
    company_id   UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    vehicle_id   UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    addon_code   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (vehicle_id, addon_code),
    FOREIGN KEY (company_id, addon_code) REFERENCES addon_catalog(company_id, code) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS vehicle_addons_vehicle_idx ON vehicle_addons(vehicle_id);
