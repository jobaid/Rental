package handlers

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Minimal single-page PDF builder (text only, Helvetica).
// Pure Go, no external dependencies. Good enough for an agreement, invoice,
// or inspection summary; swap for gofpdf/wkhtmltopdf when design matters.
type PDFBuilder struct {
	Lines []string
}

func (p *PDFBuilder) Line(s string) { p.Lines = append(p.Lines, s) }
func (p *PDFBuilder) Blank()        { p.Lines = append(p.Lines, "") }
func (p *PDFBuilder) Heading(s string) {
	p.Line(strings.ToUpper(s))
	p.Line(strings.Repeat("=", len(s)))
}

// Build renders a %PDF-1.4 document with Courier text at 10pt.
// Returns the raw bytes.
func (p *PDFBuilder) Build(title string) []byte {
	// Build the content stream.
	var cs bytes.Buffer
	fmt.Fprintln(&cs, "BT /F1 10 Tf 50 760 Td")
	fmt.Fprintln(&cs, "(", escape(title), ") Tj")
	fmt.Fprintln(&cs, "0 -16 Td")
	for _, ln := range p.Lines {
		fmt.Fprintf(&cs, "(%s) Tj\n0 -14 Td\n", escape(ln))
	}
	fmt.Fprintln(&cs, "ET")

	stream := cs.String()
	// Object offsets must be tracked for the xref.
	var out bytes.Buffer
	offsets := []int{}

	write := func(s string) { out.WriteString(s) }

	write("%PDF-1.4\n")

	addObj := func(body string) {
		offsets = append(offsets, out.Len())
		write(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", len(offsets), body))
	}

	// 1: Catalog -> 2 pages
	addObj("<< /Type /Catalog /Pages 2 0 R >>")
	// 2: Pages
	addObj("<< /Type /Pages /Count 1 /Kids [3 0 R] >>")
	// 3: Page
	addObj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>")
	// 4: Content stream
	addObj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
	// 5: Font
	addObj("<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")

	// xref
	xrefOffset := out.Len()
	write(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1))
	for _, o := range offsets {
		write(fmt.Sprintf("%010d 00000 n \n", o))
	}
	write(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xrefOffset))
	return out.Bytes()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "(", "\\(")
	s = strings.ReplaceAll(s, ")", "\\)")
	// Strip non-ASCII to stay safe with the WinAnsiEncoding built in.
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

// AgreementPDF: downloads a rental agreement for a given reservation.
func (d *Deps) AgreementPDF(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var booking, status, vehicle, customer string
	var pickup, ret time.Time
	var daily, days, total int
	var company string
	err := d.DB.Pool.QueryRow(r.Context(), `
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
	`, c.CompanyID, id).Scan(&booking, &status, &vehicle, &customer, &pickup, &ret, &daily, &days, &total, &company)
	if err != nil {
		writeErr(w, http.StatusNotFound, "booking not found")
		return
	}
	p := &PDFBuilder{}
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
	w.Write(p.Build("Rental Agreement — " + booking))
}

func (d *Deps) InvoicePDF(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var booking, vehicle, customer, company string
	var subtotal, extras, tax, total, paid int
	err := d.DB.Pool.QueryRow(r.Context(), `
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
	`, c.CompanyID, id).Scan(&booking, &vehicle, &customer, &company, &subtotal, &extras, &tax, &total, &paid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "booking not found")
		return
	}
	p := &PDFBuilder{}
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
	w.Write(p.Build("Invoice — " + booking))
}

func (d *Deps) InspectionPDF(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rid := chi.URLParam(r, "id") // reservation id
	type ins struct {
		Kind     string
		Odo      *int
		Fuel     *int
		Dmg      *string
		Created  time.Time
	}
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT kind, odometer_km, fuel_level_pct, damage_notes, created_at
		FROM inspections WHERE company_id=$1 AND reservation_id=$2
		ORDER BY created_at ASC
	`, c.CompanyID, rid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	inspections := []ins{}
	for rows.Next() {
		var i ins
		if err := rows.Scan(&i.Kind, &i.Odo, &i.Fuel, &i.Dmg, &i.Created); err == nil {
			inspections = append(inspections, i)
		}
	}
	var booking string
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT booking_number FROM reservations WHERE id=$1`, rid).Scan(&booking)

	p := &PDFBuilder{}
	p.Heading("Inspection report — " + booking)
	p.Blank()
	for _, i := range inspections {
		p.Line("Inspection: " + i.Kind + "   " + i.Created.Format(time.RFC1123))
		if i.Odo != nil {
			p.Line(fmt.Sprintf("  Odometer:  %d km", *i.Odo))
		}
		if i.Fuel != nil {
			p.Line(fmt.Sprintf("  Fuel:      %d%%", *i.Fuel))
		}
		if i.Dmg != nil && *i.Dmg != "" {
			p.Line("  Damage:    " + *i.Dmg)
		}
		p.Blank()
	}
	if len(inspections) == 0 {
		p.Line("No inspections recorded.")
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="inspection-`+booking+`.pdf"`)
	w.Write(p.Build("Inspection report"))
}
