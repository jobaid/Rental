-- Wire toll transactions into invoices: each invoice_item can be backed by a
-- toll row, and we enforce that the same toll never gets billed twice on the
-- same invoice.
ALTER TABLE invoice_items
    ADD COLUMN IF NOT EXISTS toll_transaction_id UUID REFERENCES toll_transactions(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS invoice_items_one_toll_per_invoice
    ON invoice_items(invoice_id, toll_transaction_id)
    WHERE toll_transaction_id IS NOT NULL;

-- And on the toll side, remember which invoice a toll got billed on so admin
-- actions (remove from invoice, re-sync) stay idempotent.
ALTER TABLE toll_transactions
    ADD COLUMN IF NOT EXISTS invoice_id UUID REFERENCES invoices(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS invoiced_at TIMESTAMPTZ;
