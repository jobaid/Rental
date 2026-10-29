-- Photos attached to a single inspection (pre or post), plus damage photos.
-- Fleet staff opens a tokenised link, fills odometer/fuel/damage notes and
-- uploads one or many photos per slot.
CREATE TABLE IF NOT EXISTS inspection_photos (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id     UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    inspection_id  UUID NOT NULL REFERENCES inspections(id) ON DELETE CASCADE,
    slot           TEXT NOT NULL,   -- front | rear | left | right | interior | odometer | damage
    file_path      TEXT NOT NULL,
    caption        TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS inspection_photos_inspection_idx ON inspection_photos(inspection_id);
