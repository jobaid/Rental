package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rentalsoftware/backend/internal/handlers"
)

// Admin-session PDF handlers. The /api/manager/*.pdf routes require a JWT
// bearer token, so the admin UI (session-cookie auth) cannot hit them from a
// plain <a href>. These shims generate the same PDFs scoped to the session's
// company id.

func (h *Handler) AdminAgreementPDF(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	ridStr := chi.URLParam(r, "id")
	rid, perr := uuid.Parse(ridStr)
	if perr != nil {
		http.Error(w, "bad id", 400)
		return
	}
	// Pull the manager-edited agreement template (title + body with placeholders),
	// expand placeholders for this reservation, then render as PDF.
	tpl := h.legalGet(r.Context(), cid, "agreement", defaultAgreement)
	body := h.expandLegal(r.Context(), cid, tpl.Body, rid)

	var booking string
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT booking_number FROM reservations WHERE company_id=$1 AND id=$2`,
		cid, rid).Scan(&booking)
	if booking == "" {
		http.Error(w, "booking not found", 404)
		return
	}

	p := &handlers.PDFBuilder{}
	p.Heading(tpl.Title)
	p.Blank()
	// Render the body line-by-line so blank lines become blank lines.
	for _, ln := range strings.Split(body, "\n") {
		if strings.TrimSpace(ln) == "" {
			p.Blank()
		} else {
			p.Line(ln)
		}
	}
	_ = time.Now // keep import used

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="agreement-`+booking+`.pdf"`)
	_, _ = w.Write(p.Build(tpl.Title + " — " + booking))
}

// AdminInvoicePDF — kept at its original URL for backwards compatibility, but
// now serves the modern professional invoice (which reads from invoice_items
// and therefore includes tolls). Picks the reservation's post invoice when
// available, otherwise the pre invoice.
func (h *Handler) AdminInvoicePDF(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rid := chi.URLParam(r, "id")
	var invID uuid.UUID
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id FROM invoices
		 WHERE company_id=$1 AND reservation_id=$2::uuid
		 ORDER BY CASE kind WHEN 'post' THEN 0 ELSE 1 END
		 LIMIT 1
	`, cid, rid).Scan(&invID)
	if err == nil {
		inv, lerr := h.loadInvoice(r.Context(), cid, invID)
		if lerr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(h.renderInvoiceHTML(inv, "", false)))
			return
		}
	}
	// Fall back to the old text PDF only when no invoice row exists yet
	// (shouldn't happen since booking creation drafts a pre-invoice).
	id := rid
	var booking, vehicle, customer, company string
	var subtotal, extras, tax, total, paid int
	err = h.Pool.QueryRow(r.Context(), `
		SELECT r.booking_number,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       COALESCE(cu.first_name||' '||cu.last_name,'(walk-in)'),
		       co.name,
		       r.subtotal_cents, r.extras_cents, r.tax_cents, r.total_cents, r.paid_cents
		FROM reservations r
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		JOIN companies co ON co.id=r.company_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, id).Scan(&booking, &vehicle, &customer, &company, &subtotal, &extras, &tax, &total, &paid)
	if err != nil {
		http.Error(w, "booking not found", 404)
		return
	}
	p := &handlers.PDFBuilder{}
	p.Heading(company)
	p.Blank()
	p.Line("Invoice for booking " + booking)
	p.Line("Customer: " + customer)
	p.Line("Vehicle:  " + vehicle)
	p.Blank()
	p.Line(fmt.Sprintf("Rental subtotal: $%.2f", float64(subtotal)/100))
	p.Line(fmt.Sprintf("Extras:          $%.2f", float64(extras)/100))
	p.Line(fmt.Sprintf("Tax:             $%.2f", float64(tax)/100))
	p.Line(fmt.Sprintf("Total:           $%.2f", float64(total)/100))
	p.Line(fmt.Sprintf("Paid:            $%.2f", float64(paid)/100))
	p.Line(fmt.Sprintf("Balance due:     $%.2f", float64(total-paid)/100))

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="invoice-`+booking+`.pdf"`)
	_, _ = w.Write(p.Build("Invoice — " + booking))
}
