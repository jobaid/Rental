-- Extend customers with the fields the detail page shows.
ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS street2  TEXT,
    ADD COLUMN IF NOT EXISTS state    TEXT,
    ADD COLUMN IF NOT EXISTS zip      TEXT,
    ADD COLUMN IF NOT EXISTS birthday DATE;

-- A customer's related contacts (employer, spouse, emergency contact, etc.).
CREATE TABLE IF NOT EXISTS customer_relations (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id      UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    related_contact TEXT NOT NULL,
    relation        TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS customer_relations_customer_idx ON customer_relations(customer_id);
