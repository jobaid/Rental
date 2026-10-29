package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// =============================================================================
// Types
// =============================================================================

type InvoiceItem struct {
	ID             uuid.UUID
	Description    string
	Quantity       float64 // display qty (quantity_x100 / 100)
	QuantityX100   int
	UnitPriceCents int
	AmountCents    int
	SortOrder      int
}

type Invoice struct {
	ID             uuid.UUID
	CompanyID      uuid.UUID
	ReservationID  uuid.UUID
	Kind           string // "pre" or "post"
	Number         string
	Status         string
	SubtotalCents  int
	TaxCents       int
	TotalCents     int
	TaxLabel       string
	TaxRatePct     int
	Notes          *string
	Terms          *string
	PublicToken    string
	IssuedAt       *time.Time
	SentAt         *time.Time
	PaidAt         *time.Time
	CreatedAt      time.Time
	Items          []InvoiceItem

	// Decorated fields for templates.
	BookingNumber   string
	CustomerName    string
	CustomerEmail   string
	CustomerPhone   string
	CustomerAddress string
	CustomerLicense string
	VehicleLabel    string
	VehiclePlate    string
	VehicleVIN      string
	VehicleColor    string
	VehicleFuel     string
	VehicleTrans    string
	PickupAt        time.Time
	ReturnAt        time.Time
	PickupBranch    string
	ReturnBranch    string
	CheckoutOdoKm   *int
	CheckinOdoKm    *int
	CheckoutFuelPct *int
	CheckinFuelPct  *int
	CompanyName     string
	BranchName      string
	BranchAddr      string
	InsuranceName   string
	InsuranceDesc   string
	TookInsurance   bool
}

func (i Invoice) KindLabel() string {
	if i.Kind == "pre" {
		return "Pre-Rental Invoice"
	}
	return "Final Invoice"
}

func (i Invoice) BalanceCents() int {
	return i.TotalCents // informational; paid tracked on reservation
}

// =============================================================================
// Generation (called from booking + checkin flows)
// =============================================================================

// EnsurePreInvoice is called after a booking is created. It assembles the
// line items from the reservation + addons + insurance, inserts the invoice as
// a draft, and returns the id. Called on best-effort — any failure is logged
// but does not break the booking flow.
func (h *Handler) EnsurePreInvoice(ctx context.Context, cid, resID uuid.UUID) (uuid.UUID, error) {
	// Short-circuit if one already exists.
	var existing uuid.UUID
	if err := h.Pool.QueryRow(ctx,
		`SELECT id FROM invoices WHERE reservation_id=$1 AND kind='pre'`, resID,
	).Scan(&existing); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}

	// Pull reservation + insurance context.
	var (
		booking    string
		dailyRate  int
		days       int
		subtotal   int
		extras     int
		taxCents   int
		total      int
		insCode    *string
		insTotal   int
	)
	err := h.Pool.QueryRow(ctx, `
		SELECT booking_number, daily_rate_cents, rental_days, subtotal_cents, extras_cents, tax_cents, total_cents,
		       insurance_code, insurance_total_cents
		FROM reservations WHERE id=$1
	`, resID).Scan(&booking, &dailyRate, &days, &subtotal, &extras, &taxCents, &total, &insCode, &insTotal)
	if err != nil {
		return uuid.Nil, err
	}

	tax := h.taxFor(ctx, cid)

	// Build line items.
	type lineItem struct{ desc string; qty int; unit int; amt int }
	var items []lineItem
	items = append(items, lineItem{
		desc: fmt.Sprintf("Vehicle rental — %d day(s) × $%.2f/day", days, float64(dailyRate)/100),
		qty:  days, unit: dailyRate, amt: dailyRate * days,
	})
	if insCode != nil && *insCode != "" && *insCode != "none" && insTotal > 0 {
		var insName, insDesc string
		_ = h.Pool.QueryRow(ctx,
			`SELECT name, COALESCE(description,'') FROM insurance_catalog WHERE company_id=$1 AND code=$2`,
			cid, *insCode).Scan(&insName, &insDesc)
		if insName == "" {
			insName = "Insurance coverage"
		}
		items = append(items, lineItem{
			desc: "Insurance — " + insName,
			qty:  1, unit: insTotal, amt: insTotal,
		})
	}
	if extras > 0 {
		items = append(items, lineItem{
			desc: "Extras & add-ons", qty: 1, unit: extras, amt: extras,
		})
	}

	// Totals (subtotal + taxable base = reservation subtotal already).
	iSub := 0
	for _, it := range items {
		iSub += it.amt
	}
	iTax := taxCents
	iTotal := total
	if iSub != subtotal+extras+insTotal {
		// Fallback: recompute tax from the items.
		iTax = iSub * tax.RatePct / 100
		iTotal = iSub + iTax
	}

	// Insurance T&C seeded as the invoice's terms if the customer took coverage.
	var termsText string
	if insCode != nil && *insCode != "" && *insCode != "none" {
		termsText = h.insuranceTerms(ctx, cid, *insCode)
	}

	// Reserve a number and insert.
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	num, err := h.nextInvoiceNumber(ctx, tx, "PRE")
	if err != nil {
		return uuid.Nil, err
	}
	token := rand16()
	var invID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO invoices (company_id, reservation_id, kind, number, status, subtotal_cents, tax_cents, total_cents,
		                     tax_label, tax_rate_pct, terms, public_token)
		VALUES ($1,$2,'pre',$3,'draft',$4,$5,$6,$7,$8,NULLIF($9,''),$10)
		RETURNING id
	`, cid, resID, num, iSub, iTax, iTotal, tax.Label, tax.RatePct, termsText, token).Scan(&invID)
	if err != nil {
		return uuid.Nil, err
	}
	for i, it := range items {
		_, err = tx.Exec(ctx, `
			INSERT INTO invoice_items (invoice_id, description, quantity_x100, unit_price_cents, amount_cents, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, invID, it.desc, it.qty*100, it.unit, it.amt, i)
		if err != nil {
			return uuid.Nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return invID, nil
}

// EnsurePostInvoice is called when a reservation is checked back in. It copies
// the pre-invoice's items as a starting point so the admin can adjust final
// charges (overages, late fees, damages, extra fuel) before issuing.
func (h *Handler) EnsurePostInvoice(ctx context.Context, cid, resID uuid.UUID) (uuid.UUID, error) {
	var existing uuid.UUID
	if err := h.Pool.QueryRow(ctx,
		`SELECT id FROM invoices WHERE reservation_id=$1 AND kind='post'`, resID,
	).Scan(&existing); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}

	// Make sure the pre invoice exists; use it as the baseline.
	preID, err := h.EnsurePreInvoice(ctx, cid, resID)
	if err != nil {
		return uuid.Nil, err
	}

	var subtotal, taxC, totalC, ratePct int
	var label string
	var terms *string
	err = h.Pool.QueryRow(ctx,
		`SELECT subtotal_cents, tax_cents, total_cents, tax_label, tax_rate_pct, terms
		   FROM invoices WHERE id=$1`, preID,
	).Scan(&subtotal, &taxC, &totalC, &label, &ratePct, &terms)
	if err != nil {
		return uuid.Nil, err
	}
	type item struct{ desc string; qtyX100, unit, amt, sort int }
	var items []item
	rows, _ := h.Pool.Query(ctx,
		`SELECT description, quantity_x100, unit_price_cents, amount_cents, sort_order
		   FROM invoice_items WHERE invoice_id=$1 ORDER BY sort_order`, preID)
	if rows != nil {
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.desc, &it.qtyX100, &it.unit, &it.amt, &it.sort); err == nil {
				items = append(items, it)
			}
		}
		rows.Close()
	}

	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	num, err := h.nextInvoiceNumber(ctx, tx, "FIN")
	if err != nil {
		return uuid.Nil, err
	}
	token := rand16()
	var invID uuid.UUID
	termsStr := ""
	if terms != nil {
		termsStr = *terms
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO invoices (company_id, reservation_id, kind, number, status, subtotal_cents, tax_cents, total_cents,
		                     tax_label, tax_rate_pct, terms, public_token)
		VALUES ($1,$2,'post',$3,'draft',$4,$5,$6,$7,$8,NULLIF($9,''),$10)
		RETURNING id
	`, cid, resID, num, subtotal, taxC, totalC, label, ratePct, termsStr, token).Scan(&invID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, it := range items {
		_, err = tx.Exec(ctx, `
			INSERT INTO invoice_items (invoice_id, description, quantity_x100, unit_price_cents, amount_cents, sort_order)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, invID, it.desc, it.qtyX100, it.unit, it.amt, it.sort)
		if err != nil {
			return uuid.Nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return invID, nil
}

func (h *Handler) nextInvoiceNumber(ctx context.Context, tx pgx.Tx, prefix string) (string, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT nextval('invoices_number_seq')`).Scan(&n); err != nil {
		return "", err
	}
	return fmt.Sprintf("INV-%s-%06d", prefix, n), nil
}

// insuranceTerms returns a default T&C block to attach to an invoice whose
// customer took company coverage. Admins can edit this per-invoice in the editor.
func (h *Handler) insuranceTerms(ctx context.Context, cid uuid.UUID, code string) string {
	var raw []byte
	if err := h.Pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE company_id=$1 AND key='insurance_terms'`, cid,
	).Scan(&raw); err == nil {
		var m map[string]any
		if err := jsonUnmarshal(raw, &m); err == nil {
			if v, ok := m["text"].(string); ok && v != "" {
				return v
			}
		}
	}
	var name, desc string
	var deductible int
	_ = h.Pool.QueryRow(ctx,
		`SELECT name, COALESCE(description,''), deductible_cents FROM insurance_catalog WHERE company_id=$1 AND code=$2`,
		cid, code).Scan(&name, &desc, &deductible)
	if name == "" {
		name = "Company insurance"
	}
	return "INSURANCE COVERAGE — TERMS & CONDITIONS\n\n" +
		"By accepting " + name + ", the renter agrees to the following:\n\n" +
		"1. COVERAGE. " + desc + "\n\n" +
		"2. DEDUCTIBLE. The renter remains responsible for the first $" +
		fmt.Sprintf("%.2f", float64(deductible)/100) + " of any claim, per incident.\n\n" +
		"3. EXCLUSIONS. Coverage does not apply to damage caused by: driving under the influence; " +
		"unauthorized drivers; off-road use; wilful negligence; theft resulting from unlocked / unattended vehicle; " +
		"tyre, undercarriage, and interior stains or odours; loss of personal property.\n\n" +
		"4. CLAIMS. All incidents must be reported within 24 hours and accompanied by a police report where applicable. " +
		"Failure to report voids coverage.\n\n" +
		"5. GOVERNING LAW. These terms are governed by the laws of the jurisdiction of the issuing branch.\n\n" +
		"By making a payment on this invoice, the renter acknowledges having read and accepted the terms above."
}

// =============================================================================
// Loaders
// =============================================================================

func (h *Handler) loadInvoice(ctx context.Context, cid, id uuid.UUID) (*Invoice, error) {
	var inv Invoice
	var insCode *string
	var cuPhone, cuAddr, cuCity, cuCountry, cuLicense *string
	var vPlate, vVIN, vColor, vFuel, vTrans *string
	var pickupBranch, returnBranch *string
	err := h.Pool.QueryRow(ctx, `
		SELECT i.id, i.company_id, i.reservation_id, i.kind, i.number, i.status,
		       i.subtotal_cents, i.tax_cents, i.total_cents, i.tax_label, i.tax_rate_pct,
		       i.notes, i.terms, i.public_token, i.issued_at, i.sent_at, i.paid_at, i.created_at,
		       r.booking_number,
		       COALESCE(cu.first_name||' '||cu.last_name,''),
		       COALESCE(cu.email,''),
		       cu.phone, cu.address, cu.city, cu.country, cu.license_number,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.license_plate, v.vin, v.color_exterior, v.fuel_type, v.transmission,
		       r.pickup_at, r.return_at,
		       pb.name, rb.name,
		       r.checkout_odometer_km, r.checkin_odometer_km, r.checkout_fuel_pct, r.checkin_fuel_pct,
		       r.insurance_code,
		       co.name
		FROM invoices i
		JOIN reservations r ON r.id=i.reservation_id
		JOIN companies co ON co.id=i.company_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN branches pb ON pb.id=r.pickup_branch_id
		LEFT JOIN branches rb ON rb.id=r.return_branch_id
		WHERE i.company_id=$1 AND i.id=$2
	`, cid, id).Scan(&inv.ID, &inv.CompanyID, &inv.ReservationID, &inv.Kind, &inv.Number, &inv.Status,
		&inv.SubtotalCents, &inv.TaxCents, &inv.TotalCents, &inv.TaxLabel, &inv.TaxRatePct,
		&inv.Notes, &inv.Terms, &inv.PublicToken, &inv.IssuedAt, &inv.SentAt, &inv.PaidAt, &inv.CreatedAt,
		&inv.BookingNumber, &inv.CustomerName, &inv.CustomerEmail,
		&cuPhone, &cuAddr, &cuCity, &cuCountry, &cuLicense,
		&inv.VehicleLabel,
		&vPlate, &vVIN, &vColor, &vFuel, &vTrans,
		&inv.PickupAt, &inv.ReturnAt,
		&pickupBranch, &returnBranch,
		&inv.CheckoutOdoKm, &inv.CheckinOdoKm, &inv.CheckoutFuelPct, &inv.CheckinFuelPct,
		&insCode, &inv.CompanyName)
	if err != nil {
		return nil, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	inv.CustomerPhone = deref(cuPhone)
	inv.CustomerLicense = deref(cuLicense)
	parts := []string{}
	if a := deref(cuAddr); a != "" {
		parts = append(parts, a)
	}
	if c := deref(cuCity); c != "" {
		parts = append(parts, c)
	}
	if c := deref(cuCountry); c != "" {
		parts = append(parts, c)
	}
	inv.CustomerAddress = strings.Join(parts, ", ")
	inv.VehiclePlate = deref(vPlate)
	inv.VehicleVIN = deref(vVIN)
	inv.VehicleColor = deref(vColor)
	inv.VehicleFuel = deref(vFuel)
	inv.VehicleTrans = deref(vTrans)
	inv.PickupBranch = deref(pickupBranch)
	inv.ReturnBranch = deref(returnBranch)
	if insCode != nil && *insCode != "" && *insCode != "none" {
		inv.TookInsurance = true
		_ = h.Pool.QueryRow(ctx,
			`SELECT name, COALESCE(description,'') FROM insurance_catalog WHERE company_id=$1 AND code=$2`,
			cid, *insCode).Scan(&inv.InsuranceName, &inv.InsuranceDesc)
	}
	// Primary branch for the from-address block.
	_ = h.Pool.QueryRow(ctx,
		`SELECT name, COALESCE(address,'')||CASE WHEN city IS NULL OR city='' THEN '' ELSE ', '||city END
		   FROM branches WHERE company_id=$1 AND is_active ORDER BY created_at LIMIT 1`,
		cid).Scan(&inv.BranchName, &inv.BranchAddr)
	// Items.
	rows, err := h.Pool.Query(ctx, `
		SELECT id, description, quantity_x100, unit_price_cents, amount_cents, sort_order
		FROM invoice_items WHERE invoice_id=$1 ORDER BY sort_order, created_at
	`, inv.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it InvoiceItem
		if err := rows.Scan(&it.ID, &it.Description, &it.QuantityX100, &it.UnitPriceCents, &it.AmountCents, &it.SortOrder); err != nil {
			return nil, err
		}
		it.Quantity = float64(it.QuantityX100) / 100.0
		inv.Items = append(inv.Items, it)
	}
	return &inv, nil
}

func (h *Handler) invoicesForReservation(ctx context.Context, cid, resID uuid.UUID) []Invoice {
	rows, err := h.Pool.Query(ctx, `
		SELECT id, kind, number, status, total_cents, issued_at, sent_at
		FROM invoices WHERE company_id=$1 AND reservation_id=$2
		ORDER BY CASE kind WHEN 'pre' THEN 0 ELSE 1 END
	`, cid, resID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Invoice{}
	for rows.Next() {
		var i Invoice
		if err := rows.Scan(&i.ID, &i.Kind, &i.Number, &i.Status, &i.TotalCents, &i.IssuedAt, &i.SentAt); err == nil {
			out = append(out, i)
		}
	}
	return out
}

// =============================================================================
// Admin HTTP handlers
// =============================================================================

func (h *Handler) InvoiceEditor(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	h.render(w, r, "admin/invoice_edit.html", "Invoice "+inv.Number, "reservations", map[string]any{
		"Inv":   inv,
		"Flash": r.URL.Query().Get("flash"),
		"Err":   r.URL.Query().Get("err"),
	})
}

func (h *Handler) InvoiceSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	ctx := r.Context()

	// Collect item form values. The editor uses name="item_<id-or-new-idx>_desc" etc.
	type itemIn struct {
		id       string
		desc     string
		qtyX100  int
		unit     int
		amount   int
		delete   bool
		sort     int
	}
	byKey := map[string]*itemIn{}
	keys := []string{}
	for k, v := range r.Form {
		if !strings.HasPrefix(k, "item_") || len(v) == 0 {
			continue
		}
		rest := strings.TrimPrefix(k, "item_")
		// rest = <key>_<field>
		idx := strings.LastIndex(rest, "_")
		if idx < 0 {
			continue
		}
		key := rest[:idx]
		field := rest[idx+1:]
		it, ok := byKey[key]
		if !ok {
			it = &itemIn{}
			byKey[key] = it
			keys = append(keys, key)
		}
		switch field {
		case "id":
			it.id = v[0]
		case "desc":
			it.desc = strings.TrimSpace(v[0])
		case "qty":
			f, _ := strconv.ParseFloat(strings.TrimSpace(v[0]), 64)
			it.qtyX100 = int(f * 100)
		case "unit":
			it.unit = parseDollars(v[0])
		case "amount":
			it.amount = parseDollars(v[0])
		case "delete":
			it.delete = v[0] == "1" || v[0] == "on"
		case "sort":
			it.sort, _ = strconv.Atoi(v[0])
		}
	}

	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer tx.Rollback(ctx)

	// Make sure this invoice belongs to our company.
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM invoices WHERE company_id=$1 AND id=$2`, cid, id).Scan(&status); err != nil {
		http.NotFound(w, r)
		return
	}

	// Upsert / delete each item; recompute totals.
	subtotal := 0
	for sortIdx, key := range keys {
		it := byKey[key]
		if it.desc == "" && it.amount == 0 && it.unit == 0 && it.qtyX100 == 0 {
			continue
		}
		if it.qtyX100 == 0 {
			it.qtyX100 = 100
		}
		// Prefer explicit amount; otherwise qty*unit.
		if it.amount == 0 {
			it.amount = it.unit * it.qtyX100 / 100
		}
		if it.id != "" && !strings.HasPrefix(key, "new") {
			if it.delete {
				_, _ = tx.Exec(ctx, `DELETE FROM invoice_items WHERE invoice_id=$1 AND id=$2`, id, it.id)
				continue
			}
			_, err = tx.Exec(ctx, `
				UPDATE invoice_items SET description=$1, quantity_x100=$2, unit_price_cents=$3, amount_cents=$4, sort_order=$5
				WHERE invoice_id=$6 AND id=$7
			`, it.desc, it.qtyX100, it.unit, it.amount, sortIdx, id, it.id)
			if err != nil {
				http.Error(w, "update item: "+err.Error(), 500)
				return
			}
			subtotal += it.amount
		} else {
			if it.delete {
				continue
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO invoice_items (invoice_id, description, quantity_x100, unit_price_cents, amount_cents, sort_order)
				VALUES ($1,$2,$3,$4,$5,$6)
			`, id, it.desc, it.qtyX100, it.unit, it.amount, sortIdx)
			if err != nil {
				http.Error(w, "insert item: "+err.Error(), 500)
				return
			}
			subtotal += it.amount
		}
	}

	// Header fields.
	notes := strings.TrimSpace(r.FormValue("notes"))
	terms := r.FormValue("terms")
	taxLabel := strings.TrimSpace(r.FormValue("taxLabel"))
	if taxLabel == "" {
		taxLabel = "Tax"
	}
	taxRate, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("taxRatePct")))
	if taxRate < 0 {
		taxRate = 0
	}
	tax := subtotal * taxRate / 100
	total := subtotal + tax

	_, err = tx.Exec(ctx, `
		UPDATE invoices
		   SET subtotal_cents=$1, tax_cents=$2, total_cents=$3,
		       tax_label=$4, tax_rate_pct=$5,
		       notes=NULLIF($6,''), terms=NULLIF($7,''),
		       updated_at=NOW()
		 WHERE company_id=$8 AND id=$9
	`, subtotal, tax, total, taxLabel, taxRate, notes, terms, cid, id)
	if err != nil {
		http.Error(w, "update invoice: "+err.Error(), 500)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "commit: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/invoices/"+idStr+"?flash=Saved", http.StatusSeeOther)
}

// parseDollars reads "12.34" or "1234" (cents) and returns cents.
func parseDollars(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.Contains(s, ".") {
		f, _ := strconv.ParseFloat(s, 64)
		return int(f*100 + 0.5)
	}
	n, _ := strconv.Atoi(s)
	return n
}

func (h *Handler) InvoiceStatus(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	status := r.FormValue("status")
	switch status {
	case "issued":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE invoices SET status='issued', issued_at=COALESCE(issued_at,NOW()), updated_at=NOW() WHERE company_id=$1 AND id=$2`,
			cid, idStr)
	case "paid":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE invoices SET status='paid', paid_at=NOW(), updated_at=NOW() WHERE company_id=$1 AND id=$2`,
			cid, idStr)
	case "void":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE invoices SET status='void', updated_at=NOW() WHERE company_id=$1 AND id=$2`,
			cid, idStr)
	}
	http.Redirect(w, r, "/admin/invoices/"+idStr+"?flash=Status+updated", http.StatusSeeOther)
}

func (h *Handler) InvoiceSend(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_ = r.ParseForm()
	to := strings.TrimSpace(r.FormValue("to"))
	if to == "" {
		to = inv.CustomerEmail
	}
	if to == "" {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err=No+email+on+file", http.StatusSeeOther)
		return
	}
	subject := strings.TrimSpace(r.FormValue("subject"))
	if subject == "" {
		subject = inv.KindLabel() + " " + inv.Number + " — " + inv.CompanyName
	}
	message := r.FormValue("message")

	htmlBody := h.renderInvoiceHTML(inv, message, true)
	textBody := invoicePlainText(inv, message)

	m := h.mailerFor(r.Context(), cid)
	if !m.Cfg.Configured() {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err=SMTP+not+configured", http.StatusSeeOther)
		return
	}
	if _, err := m.SendHTML(to, subject, htmlBody, textBody); err != nil {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err="+err.Error(), http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE invoices SET status=CASE WHEN status='draft' THEN 'sent' ELSE status END,
		 sent_at=NOW(), issued_at=COALESCE(issued_at,NOW()), updated_at=NOW()
		 WHERE company_id=$1 AND id=$2`, cid, idStr)
	http.Redirect(w, r, "/admin/invoices/"+idStr+"?flash=Invoice+emailed+to+"+to, http.StatusSeeOther)
}

func (h *Handler) InvoicePrint(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h.renderInvoiceHTML(inv, "", false)))
}

// Public read-only view via token.
func (h *Handler) InvoicePublic(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var id, cid uuid.UUID
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT id, company_id FROM invoices WHERE public_token=$1`, tok,
	).Scan(&id, &cid); err != nil {
		http.NotFound(w, r)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(h.renderInvoiceHTML(inv, "", false)))
}

// =============================================================================
// Rendering
// =============================================================================

const invoiceCSS = `
:root{--ink:#0a0a0a;--muted:#5f6b7a;--line:#d9dde3;--softline:#eef1f5;--band:#f6f7f9;--accent:#0b3d2e;--accent-ink:#ffffff;--grand:#0a0a0a;--mono:ui-monospace,"SF Mono",Menlo,Consolas,monospace}
*{box-sizing:border-box}
body{margin:0;background:#eceef2;color:var(--ink);font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Helvetica Neue",Arial,sans-serif;line-height:1.45;font-size:13.5px;-webkit-font-smoothing:antialiased}
.sheet{max-width:900px;margin:28px auto;background:#fff;border:1px solid var(--line);box-shadow:0 1px 2px rgba(0,0,0,.05)}
/* Header band */
.band{background:var(--accent);color:var(--accent-ink);padding:22px 44px;display:flex;justify-content:space-between;align-items:center;gap:24px}
.band .brand{display:flex;align-items:center;gap:14px}
.band .mark{width:44px;height:44px;border:2px solid var(--accent-ink);display:flex;align-items:center;justify-content:center;font-weight:800;font-size:18px;letter-spacing:1px;border-radius:4px}
.band .co{font-size:19px;font-weight:700;letter-spacing:.3px}
.band .co small{display:block;font-weight:400;font-size:11px;opacity:.78;letter-spacing:1.5px;text-transform:uppercase;margin-top:2px}
.band .doc{text-align:right}
.band .doc .kind{font-size:11px;letter-spacing:3px;text-transform:uppercase;opacity:.82}
.band .doc .num{font-family:var(--mono);font-size:16px;font-weight:600;margin-top:2px}
.band .doc .date{font-size:11.5px;opacity:.82;margin-top:4px}
/* Status band */
.status-strip{display:flex;justify-content:space-between;align-items:center;padding:9px 44px;border-bottom:1px solid var(--line);background:var(--band);font-size:12px;letter-spacing:.3px;color:var(--muted)}
.status-strip .pill{display:inline-block;padding:3px 11px;border-radius:2px;font-size:10.5px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;background:#e9ebf0;color:#2a2f36;margin-left:8px}
.status-strip .pill.issued{background:#e3edff;color:#0d3a99}
.status-strip .pill.sent{background:#fff4d6;color:#7a5300}
.status-strip .pill.paid{background:#d7f0dd;color:#145a2b}
.status-strip .pill.void{background:#fde0e0;color:#8a1b1b}
.status-strip .pill.draft{background:#eceff3;color:#4a515a}
/* Section titles */
.sec-title{font-size:11px;letter-spacing:2.5px;text-transform:uppercase;color:var(--accent);font-weight:700;padding:14px 44px 6px;border-top:1px solid var(--softline);background:#fff}
.sec-title:first-of-type{border-top:0}
/* 3-up info blocks */
.blocks{display:grid;grid-template-columns:1fr 1fr 1fr;gap:0;border-top:1px solid var(--softline);border-bottom:1px solid var(--softline)}
.blocks .b{padding:14px 24px;border-right:1px solid var(--softline)}
.blocks .b:last-child{border-right:0}
.blocks .b:first-child{padding-left:44px}
.blocks .b:last-child{padding-right:44px}
.b .lbl{font-size:10.5px;letter-spacing:1.5px;text-transform:uppercase;color:var(--muted);margin-bottom:5px}
.b .val{font-weight:600;font-size:13px}
.b .sub{color:var(--muted);font-size:12.5px;margin-top:2px}
.b .mono{font-family:var(--mono);font-size:12.5px}
/* key:value grid used in renter/vehicle panels */
.kvgrid{display:grid;grid-template-columns:150px 1fr;row-gap:5px;column-gap:12px;font-size:12.5px;padding:10px 44px 20px}
.kvgrid .k{color:var(--muted);text-transform:uppercase;letter-spacing:1px;font-size:10.5px;padding-top:2px}
.kvgrid .v{color:var(--ink)}
.kvgrid .v.mono{font-family:var(--mono)}
.two-col{display:grid;grid-template-columns:1fr 1fr;gap:0}
.two-col > div{padding:0}
.two-col .kvgrid{padding-right:24px}
.two-col > div + div .kvgrid{padding-left:24px;border-left:1px solid var(--softline)}
.two-col > div:first-child .kvgrid{padding-left:44px}
.two-col > div:last-child .kvgrid{padding-right:44px}
/* Line-item table */
table.lines{width:100%;border-collapse:collapse;margin:0}
table.lines th{text-align:left;font-size:10.5px;letter-spacing:1.5px;color:var(--muted);text-transform:uppercase;background:var(--band);padding:10px 10px;border-top:1px solid var(--line);border-bottom:1px solid var(--line);font-weight:600}
table.lines th:first-child{padding-left:44px}
table.lines th:last-child{padding-right:44px}
table.lines th.right,table.lines td.right{text-align:right}
table.lines td{padding:11px 10px;border-bottom:1px solid var(--softline);vertical-align:top}
table.lines td:first-child{padding-left:44px}
table.lines td:last-child{padding-right:44px}
table.lines tbody tr:nth-child(even) td{background:#fcfcfd}
table.lines td.desc{font-weight:500}
table.lines td.num{font-variant-numeric:tabular-nums}
/* Totals */
.totals-wrap{display:flex;justify-content:flex-end;padding:14px 44px 6px}
.totals{width:360px;font-variant-numeric:tabular-nums}
.totals .r{display:flex;justify-content:space-between;padding:5px 0;color:#1f2937;font-size:13px}
.totals .r .k{color:var(--muted)}
.totals .r.sub{border-top:1px solid var(--softline);padding-top:9px;margin-top:4px}
.totals .r.grand{border-top:2px solid var(--grand);border-bottom:3px double var(--grand);padding:11px 0;margin-top:6px;font-size:17px;font-weight:800}
/* Notes + terms */
.msg{margin:12px 44px 0;padding:12px 16px;background:#f3f8ff;border-left:3px solid #2b5fd0;color:#243a66;font-size:13px;white-space:pre-wrap}
.notes{margin:14px 44px 0;padding:12px 16px;background:var(--band);border-left:3px solid var(--accent);font-size:12.5px;color:#2a2f36;white-space:pre-wrap}
.terms{margin:18px 44px 0;padding:14px 18px;border:1px solid var(--line);background:#fcfcfd;font-size:11.5px;color:#2a2f36;white-space:pre-wrap;line-height:1.55}
.terms h4{margin:0 0 8px;font-size:10.5px;letter-spacing:2px;text-transform:uppercase;color:var(--accent);font-weight:700}
.terms .ack{margin-top:12px;padding-top:10px;border-top:1px dashed var(--line);color:var(--muted);font-size:11px}
/* Signature block */
.signatures{display:grid;grid-template-columns:1fr 1fr;gap:30px;padding:30px 44px 24px;border-top:1px solid var(--softline);margin-top:18px}
.signatures .line{border-top:1px solid var(--ink);padding-top:6px;font-size:11px;letter-spacing:1.5px;text-transform:uppercase;color:var(--muted)}
/* Footer */
.foot{padding:14px 44px;background:var(--band);border-top:1px solid var(--line);color:var(--muted);font-size:11px;text-align:center;letter-spacing:.4px}
.foot .num{font-family:var(--mono);color:var(--ink)}
/* Watermarks (void/paid) */
.watermark{position:relative}
.watermark::after{content:attr(data-wm);position:absolute;top:40%;left:0;right:0;text-align:center;font-size:120px;font-weight:900;letter-spacing:8px;color:rgba(10,10,10,0.05);transform:rotate(-18deg);pointer-events:none;text-transform:uppercase}
@media print{
  body{background:#fff}
  .sheet{box-shadow:none;border:0;margin:0;max-width:none}
  .band,.status-strip,table.lines th{-webkit-print-color-adjust:exact;print-color-adjust:exact}
}
`

// renderInvoiceHTML builds an enterprise-grade HTML invoice used for the
// email body, the printable preview, and the public token page.
// `message` (optional) is the admin's cover note added in the Send dialog.
func (h *Handler) renderInvoiceHTML(inv *Invoice, message string, forEmail bool) string {
	esc := html.EscapeString
	fmtD := func(c int) string { return fmt.Sprintf("$%.2f", float64(c)/100) }
	or := func(s, fallback string) string {
		if s == "" {
			return fallback
		}
		return s
	}

	// Compute rental duration in days (ceil).
	days := int(inv.ReturnAt.Sub(inv.PickupAt).Hours() / 24)
	if inv.ReturnAt.Sub(inv.PickupAt).Hours()-float64(days)*24 > 0 {
		days++
	}
	if days < 1 {
		days = 1
	}

	var buf bytes.Buffer
	buf.WriteString("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>")
	buf.WriteString(esc(inv.Number) + " — " + esc(inv.CompanyName))
	buf.WriteString("</title><style>")
	buf.WriteString(invoiceCSS)
	buf.WriteString("</style></head><body>")

	wm := ""
	if inv.Status == "paid" {
		wm = ` class="watermark" data-wm="paid"`
	} else if inv.Status == "void" {
		wm = ` class="watermark" data-wm="void"`
	}
	buf.WriteString("<div class=\"sheet\"" + wm + ">")

	// ========== HEADER BAND ==========
	mark := strings.ToUpper(firstRunes(inv.CompanyName, 2))
	if mark == "" {
		mark = "MR"
	}
	issued := inv.CreatedAt
	if inv.IssuedAt != nil {
		issued = *inv.IssuedAt
	}
	buf.WriteString(`<div class="band"><div class="brand"><div class="mark">` + esc(mark) + `</div><div class="co">` + esc(inv.CompanyName))
	buf.WriteString(`<small>Vehicle Rental Services</small></div></div>`)
	buf.WriteString(`<div class="doc"><div class="kind">` + esc(strings.ToUpper(inv.KindLabel())) + `</div>`)
	buf.WriteString(`<div class="num">` + esc(inv.Number) + `</div>`)
	buf.WriteString(`<div class="date">Issued ` + issued.Format("02 Jan 2006") + `</div></div></div>`)

	// ========== STATUS STRIP ==========
	buf.WriteString(`<div class="status-strip"><div>Reservation <strong style="color:#0a0a0a;font-family:var(--mono);">` + esc(inv.BookingNumber) + `</strong></div>`)
	buf.WriteString(`<div>Status <span class="pill ` + esc(inv.Status) + `">` + esc(inv.Status) + `</span></div></div>`)

	if message != "" {
		buf.WriteString(`<div class="msg">` + esc(message) + `</div>`)
	}

	// ========== TOP SUMMARY BLOCKS ==========
	buf.WriteString(`<div class="sec-title">Rental Summary</div>`)
	buf.WriteString(`<div class="blocks">`)
	buf.WriteString(`<div class="b"><div class="lbl">Rental Period</div><div class="val">` + inv.PickupAt.Format("Mon 02 Jan 2006") + `</div><div class="sub">` + inv.PickupAt.Format("15:04") + ` → ` + inv.ReturnAt.Format("Mon 02 Jan 2006 · 15:04") + `</div><div class="sub">` + strconv.Itoa(days) + ` day(s)</div></div>`)
	buf.WriteString(`<div class="b"><div class="lbl">Pickup Location</div><div class="val">` + esc(or(inv.PickupBranch, or(inv.BranchName, inv.CompanyName))) + `</div><div class="sub">` + esc(or(inv.BranchAddr, "—")) + `</div></div>`)
	buf.WriteString(`<div class="b"><div class="lbl">Return Location</div><div class="val">` + esc(or(inv.ReturnBranch, or(inv.PickupBranch, inv.BranchName))) + `</div><div class="sub">` + esc(or(inv.BranchAddr, "—")) + `</div></div>`)
	buf.WriteString(`</div>`)

	// ========== RENTER & VEHICLE (two columns) ==========
	buf.WriteString(`<div class="two-col">`)
	// Renter
	buf.WriteString(`<div><div class="sec-title">Renter Information</div><div class="kvgrid">`)
	buf.WriteString(`<div class="k">Full name</div><div class="v">` + esc(or(inv.CustomerName, "—")) + `</div>`)
	buf.WriteString(`<div class="k">Email</div><div class="v">` + esc(or(inv.CustomerEmail, "—")) + `</div>`)
	buf.WriteString(`<div class="k">Phone</div><div class="v">` + esc(or(inv.CustomerPhone, "—")) + `</div>`)
	buf.WriteString(`<div class="k">Address</div><div class="v">` + esc(or(inv.CustomerAddress, "—")) + `</div>`)
	buf.WriteString(`<div class="k">Driver's License</div><div class="v mono">` + esc(or(inv.CustomerLicense, "—")) + `</div>`)
	buf.WriteString(`</div></div>`)
	// Vehicle
	buf.WriteString(`<div><div class="sec-title">Vehicle</div><div class="kvgrid">`)
	buf.WriteString(`<div class="k">Make / Model</div><div class="v">` + esc(or(strings.TrimSpace(inv.VehicleLabel), "—")) + `</div>`)
	buf.WriteString(`<div class="k">License plate</div><div class="v mono">` + esc(or(inv.VehiclePlate, "—")) + `</div>`)
	buf.WriteString(`<div class="k">VIN</div><div class="v mono">` + esc(or(inv.VehicleVIN, "—")) + `</div>`)
	buf.WriteString(`<div class="k">Color / Fuel</div><div class="v">` + esc(or(inv.VehicleColor, "—")) + ` · ` + esc(or(inv.VehicleFuel, "—")) + `</div>`)
	// Odometer in/out summary (for post-rental).
	odoOut, odoIn := "—", "—"
	if inv.CheckoutOdoKm != nil {
		odoOut = fmt.Sprintf("%d km", *inv.CheckoutOdoKm)
	}
	if inv.CheckinOdoKm != nil {
		odoIn = fmt.Sprintf("%d km", *inv.CheckinOdoKm)
	}
	buf.WriteString(`<div class="k">Odometer</div><div class="v">Out ` + odoOut + ` · In ` + odoIn + `</div>`)
	buf.WriteString(`</div></div>`)
	buf.WriteString(`</div>`)

	// ========== CHARGES ==========
	buf.WriteString(`<div class="sec-title">Charges</div>`)
	buf.WriteString(`<table class="lines"><thead><tr><th style="width:60%;">Description</th><th class="right">Qty</th><th class="right">Rate</th><th class="right">Amount</th></tr></thead><tbody>`)
	if len(inv.Items) == 0 {
		buf.WriteString(`<tr><td colspan="4" style="text-align:center;color:#9aa2ad;padding:28px;">No line items.</td></tr>`)
	}
	for _, it := range inv.Items {
		buf.WriteString(`<tr><td class="desc">` + esc(it.Description) + `</td>`)
		buf.WriteString(`<td class="right num">` + fmt.Sprintf("%.2f", it.Quantity) + `</td>`)
		buf.WriteString(`<td class="right num">` + fmtD(it.UnitPriceCents) + `</td>`)
		buf.WriteString(`<td class="right num">` + fmtD(it.AmountCents) + `</td></tr>`)
	}
	buf.WriteString(`</tbody></table>`)

	// ========== TOTALS ==========
	buf.WriteString(`<div class="totals-wrap"><div class="totals">`)
	buf.WriteString(`<div class="r sub"><span class="k">Subtotal</span><span>` + fmtD(inv.SubtotalCents) + `</span></div>`)
	buf.WriteString(`<div class="r"><span class="k">` + esc(inv.TaxLabel) + ` (` + strconv.Itoa(inv.TaxRatePct) + `%)</span><span>` + fmtD(inv.TaxCents) + `</span></div>`)
	buf.WriteString(`<div class="r grand"><span>Total Due</span><span>` + fmtD(inv.TotalCents) + `</span></div>`)
	buf.WriteString(`</div></div>`)

	// ========== NOTES ==========
	if inv.Notes != nil && *inv.Notes != "" {
		buf.WriteString(`<div class="notes"><strong>Notes</strong><br>` + esc(*inv.Notes) + `</div>`)
	}

	// ========== TERMS & CONDITIONS ==========
	if inv.Terms != nil && *inv.Terms != "" {
		title := "Terms & Conditions"
		if inv.TookInsurance {
			title = "Insurance Coverage — Terms & Conditions"
		}
		buf.WriteString(`<div class="terms"><h4>` + title + `</h4>`)
		buf.WriteString(esc(*inv.Terms))
		buf.WriteString(`<div class="ack">By tendering payment on this invoice, the Renter acknowledges having read and accepted the terms above.</div>`)
		buf.WriteString(`</div>`)
	}

	// ========== SIGNATURE BLOCK ==========
	buf.WriteString(`<div class="signatures">`)
	buf.WriteString(`<div><div style="height:40px;"></div><div class="line">Renter signature &nbsp;·&nbsp; Date</div></div>`)
	buf.WriteString(`<div><div style="height:40px;"></div><div class="line">Authorized agent — ` + esc(inv.CompanyName) + `</div></div>`)
	buf.WriteString(`</div>`)

	// ========== FOOTER ==========
	buf.WriteString(`<div class="foot">` + esc(inv.CompanyName))
	if inv.BranchAddr != "" {
		buf.WriteString(` &nbsp;·&nbsp; ` + esc(inv.BranchAddr))
	}
	buf.WriteString(` &nbsp;·&nbsp; Invoice <span class="num">` + esc(inv.Number) + `</span> &nbsp;·&nbsp; Reservation <span class="num">` + esc(inv.BookingNumber) + `</span>`)
	buf.WriteString(`<br>Thank you for choosing ` + esc(inv.CompanyName) + `. This document is computer-generated and valid without signature.</div>`)

	buf.WriteString(`</div></body></html>`)
	return buf.String()
}

func firstRunes(s string, n int) string {
	var out []rune
	for _, r := range s {
		if r == ' ' || r == '\t' {
			continue
		}
		out = append(out, r)
		if len(out) >= n {
			break
		}
	}
	return string(out)
}

func invoicePlainText(inv *Invoice, message string) string {
	var b strings.Builder
	if message != "" {
		b.WriteString(message + "\n\n")
	}
	fmt.Fprintf(&b, "%s %s\n", inv.KindLabel(), inv.Number)
	fmt.Fprintf(&b, "Company: %s\n", inv.CompanyName)
	fmt.Fprintf(&b, "Booking: %s\n", inv.BookingNumber)
	fmt.Fprintf(&b, "Vehicle: %s\n", inv.VehicleLabel)
	fmt.Fprintf(&b, "Dates:  %s → %s\n\n", inv.PickupAt.Format("Mon 02 Jan 15:04"), inv.ReturnAt.Format("Mon 02 Jan 15:04"))
	for _, it := range inv.Items {
		fmt.Fprintf(&b, "  • %s — %.2f × $%.2f = $%.2f\n",
			it.Description, it.Quantity, float64(it.UnitPriceCents)/100, float64(it.AmountCents)/100)
	}
	fmt.Fprintf(&b, "\nSubtotal: $%.2f\n", float64(inv.SubtotalCents)/100)
	fmt.Fprintf(&b, "%s (%d%%): $%.2f\n", inv.TaxLabel, inv.TaxRatePct, float64(inv.TaxCents)/100)
	fmt.Fprintf(&b, "TOTAL DUE: $%.2f\n", float64(inv.TotalCents)/100)
	if inv.TookInsurance && inv.Terms != nil && *inv.Terms != "" {
		b.WriteString("\n---\nINSURANCE — TERMS & CONDITIONS\n")
		b.WriteString(*inv.Terms)
	}
	return b.String()
}
