package web

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rentalsoftware/backend/internal/handlers"
)

// Admin-session PDF handlers. The /api/manager/*.pdf routes require a JWT
// bearer token, so the admin UI (session-cookie auth) cannot hit them from a
// plain <a href>. These shims generate the same PDFs scoped to the session's
// company id.

func (h *Handler) AdminAgreementPDF(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	var booking, status, vehicle, customer string
	var pickup, ret time.Time
	var daily, days, total int
	var company string
	err := h.Pool.QueryRow(r.Context(), `
		SELECT r.booking_number, r.status,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       COALESCE(cu.first_name||' '||cu.last_name,'(walk-in)'),
		       r.pickup_at, r.return_at, r.daily_rate_cents, r.rental_days, r.total_cents,
		       co.name
		FROM reservations r
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		JOIN companies co ON co.id=r.company_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, id).Scan(&booking, &status, &vehicle, &customer, &pickup, &ret, &daily, &days, &total, &company)
	if err != nil {
		http.Error(w, "booking not found", 404)
		return
	}
	p := &handlers.PDFBuilder{}
	p.Heading(company)
	p.Blank()
	p.Line("Rental Agreement")
	p.Line("Booking: " + booking + "   Status: " + status)
	p.Blank()
	p.Line("Customer: " + customer)
	p.Line("Vehicle:  " + vehicle)
	p.Line("Pickup:   " + pickup.Format(time.RFC1123))
	p.Line("Return:   " + ret.Format(time.RFC1123))
	p.Blank()
	p.Heading("Charges")
	p.Line(fmt.Sprintf("Daily rate: $%.2f  x  %d day(s)", float64(daily)/100, days))
	p.Line(fmt.Sprintf("Total:      $%.2f", float64(total)/100))
	p.Blank()
	p.Heading("Terms")
	p.Line("1. Customer agrees to return the vehicle in equivalent condition.")
	p.Line("2. Customer is responsible for traffic violations during the rental.")
	p.Line("3. Additional mileage, fuel and damage charges apply per the agreed policy.")
	p.Line("4. Late return incurs the configured late fee.")
	p.Blank()
	p.Line("Signed on behalf of " + company + ": __________________________")
	p.Line("Signed by customer:               __________________________")

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="agreement-`+booking+`.pdf"`)
	_, _ = w.Write(p.Build("Rental Agreement — " + booking))
}

func (h *Handler) AdminInvoicePDF(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	var booking, vehicle, customer, company string
	var subtotal, extras, tax, total, paid int
	err := h.Pool.QueryRow(r.Context(), `
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
