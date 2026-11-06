DROP INDEX IF EXISTS invoice_items_one_toll_per_invoice;
ALTER TABLE invoice_items DROP COLUMN IF EXISTS toll_transaction_id;
ALTER TABLE toll_transactions DROP COLUMN IF EXISTS invoice_id, DROP COLUMN IF EXISTS invoiced_at;
