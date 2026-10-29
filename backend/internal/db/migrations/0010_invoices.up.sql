-- Pre & post invoices tied 1-to-1 (per kind) to each reservation.

CREATE SEQUENCE IF NOT EXISTS invoices_number_seq START 100000;

CREATE TABLE IF NOT EXISTS invoices (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    company_id     UUID NOT NULL REFERENCES companies(id) ON DELETE CASCADE,
    reservation_id UUID NOT NULL REFERENCES reservations(id) ON DELETE CASCADE,
    kind           TEXT NOT NULL CHECK (kind IN ('pre','post')),
    number         TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft','issued','sent','paid','void')),
    subtotal_cents INT NOT NULL DEFAULT 0,
    tax_cents      INT NOT NULL DEFAULT 0,
    total_cents    INT NOT NULL DEFAULT 0,
    tax_label      TEXT NOT NULL DEFAULT 'Tax',
    tax_rate_pct   INT  NOT NULL DEFAULT 0,
    notes          TEXT,
    terms          TEXT,
    public_token   TEXT UNIQUE NOT NULL,
    issued_at      TIMESTAMPTZ,
    sent_at        TIMESTAMPTZ,
    paid_at        TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (company_id, number),
    UNIQUE (reservation_id, kind)
);

CREATE INDEX IF NOT EXISTS invoices_reservation_idx ON invoices(reservation_id);

CREATE TABLE IF NOT EXISTS invoice_items (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    invoice_id       UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    description      TEXT NOT NULL,
    quantity_x100    INT  NOT NULL DEFAULT 100,  -- quantity * 100 (two decimals)
    unit_price_cents INT  NOT NULL DEFAULT 0,
    amount_cents     INT  NOT NULL DEFAULT 0,
    sort_order       INT  NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS invoice_items_invoice_idx ON invoice_items(invoice_id);
