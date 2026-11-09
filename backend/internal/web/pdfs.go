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

// AdminAgreementPDF now serves the agreement as branded HTML (red / black /
// white — matches customer UI and invoice). Admin prints to PDF from the
// browser. Keeps the old text PDF only as a last-resort fallback if there's
// no reservation row.
func (h *Handler) AdminAgreementPDF(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	ridStr := chi.URLParam(r, "id")
	rid, perr := uuid.Parse(ridStr)
	if perr != nil {
		http.Error(w, "bad id", 400)
		return
	}
	tpl := h.legalGet(r.Context(), cid, "agreement", defaultAgreement)
	body := h.expandLegal(r.Context(), cid, tpl.Body, rid)

	var booking string
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT booking_number FROM reservations WHERE company_id=$1 AND id=$2`,
		cid, rid).Scan(&booking); err != nil || booking == "" {
		http.Error(w, "booking not found", 404)
		return
	}
	brand := h.loadBrand(r.Context())
	html := renderAgreementHTML(h, tpl.Title, body, booking, brand)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
	_ = time.Now
	_ = handlers.PDFBuilder{}
	_ = strings.TrimSpace
}

// renderAgreementHTML — same visual language as the invoice: black header
// band with 4px red accent line, red section underline, printable (A4 / US
// Letter), auto-launches print dialog from the Print button on page.
func renderAgreementHTML(h *Handler, title, body, booking string, brand Brand) string {
	logo := ""
	if brand.LogoURL != "" {
		src := brand.LogoURL
		if !strings.HasPrefix(src, "http") && h.PublicBase != "" {
			src = strings.TrimRight(h.PublicBase, "/") + src
		}
		logo = `<span style="display:inline-flex;align-items:center;justify-content:center;background:linear-gradient(135deg,#0a0a0a 0%,#2a0b0b 55%,#dc2626 100%);padding:8px 14px;border-radius:8px;border:2px solid rgba(255,255,255,.18);box-shadow:0 4px 14px rgba(220,38,38,.35);min-width:80px;min-height:56px;"><img src="` + src + `" alt="` + htmlEscape(brand.Name) + `" style="display:block;max-height:52px;max-width:180px;width:auto;height:auto;object-fit:contain;filter:drop-shadow(0 0 1px rgba(255,255,255,.4));"></span>`
	}
	mark := strings.ToUpper(firstRunes(brand.Name, 2))
	if mark == "" {
		mark = "MR"
	}
	// Contact line in the band footer.
	contact := []string{}
	if brand.Phone != "" {
		contact = append(contact, "📞 "+brand.Phone)
	}
	if brand.Email != "" {
		contact = append(contact, "✉ "+brand.Email)
	}
	if brand.Address != "" {
		contact = append(contact, "📍 "+brand.Address)
	}
	contactLine := strings.Join(contact, " &nbsp;·&nbsp; ")

	var bodyHTML strings.Builder
	for _, ln := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(ln)
		if trim == "" {
			bodyHTML.WriteString(`<div style="height:10px;"></div>`)
			continue
		}
		bodyHTML.WriteString(`<p style="margin:0 0 6px;">` + htmlEscape(ln) + `</p>`)
	}

	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` + htmlEscape(title) + ` — ` + htmlEscape(booking) + `</title>
<style>
*{box-sizing:border-box}
body{margin:0;background:#eceef2;color:#0a0a0a;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Helvetica Neue",Arial,sans-serif;line-height:1.55;font-size:13.5px;-webkit-font-smoothing:antialiased}
.sheet{max-width:900px;margin:28px auto;background:#fff;border:1px solid #e5e7eb;box-shadow:0 1px 2px rgba(0,0,0,.05)}
.band{background:#0a0a0a;color:#fff;padding:22px 44px;display:flex;justify-content:space-between;align-items:center;gap:24px;border-bottom:4px solid #dc2626}
.band .brand{display:flex;align-items:center;gap:14px}
.band .mark{width:44px;height:44px;border:2px solid #fff;background:#dc2626;display:flex;align-items:center;justify-content:center;font-weight:800;font-size:18px;letter-spacing:1px;border-radius:4px}
.band .co{font-size:19px;font-weight:700;letter-spacing:.3px}
.band .co small{display:block;font-weight:400;font-size:11px;opacity:.78;letter-spacing:1.5px;text-transform:uppercase;margin-top:2px}
.band .doc{text-align:right}
.band .doc .kind{font-size:11px;letter-spacing:3px;text-transform:uppercase;opacity:.82}
.band .doc .num{font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;font-size:16px;font-weight:600;margin-top:2px;color:#dc2626}
.contact-strip{padding:8px 44px;background:#f9fafb;color:#5f6b7a;font-size:12px;border-bottom:1px solid #e5e7eb}
.title{padding:28px 44px 8px;font-size:22px;letter-spacing:-.02em;font-weight:800;color:#0a0a0a}
.title::after{content:"";display:block;width:60px;height:3px;background:#dc2626;margin-top:10px;border-radius:2px}
.body{padding:16px 44px 30px;color:#1f2937;max-width:none}
.sig{display:grid;grid-template-columns:1fr 1fr;gap:30px;padding:16px 44px 28px;border-top:1px solid #f3f4f6;margin-top:14px}
.sig .line{border-top:1px solid #0a0a0a;padding-top:6px;font-size:11px;letter-spacing:1.5px;text-transform:uppercase;color:#6b7280}
.foot{padding:14px 44px;background:#0a0a0a;color:#d1d5db;font-size:11.5px;text-align:center;border-top:4px solid #dc2626}
.foot a{color:#fff;text-decoration:none}
.action{position:fixed;top:14px;right:14px;z-index:50;display:flex;gap:8px}
.action button,.action a{background:#dc2626;color:#fff;border:0;padding:10px 16px;border-radius:8px;cursor:pointer;font-weight:600;font-size:13px;text-decoration:none;box-shadow:0 10px 24px rgba(220,38,38,.25)}
.action a.sec{background:#0a0a0a}
@media print{
  body{background:#fff}
  .sheet{box-shadow:none;border:0;margin:0;max-width:none}
  .band,.foot{-webkit-print-color-adjust:exact;print-color-adjust:exact}
  .action{display:none}
}
</style></head><body>
<div class="action">
  <a class="sec" href="javascript:history.back()">← Back</a>
  <button onclick="window.print()">🖨 Print / Save PDF</button>
</div>
<div class="sheet">
  <div class="band">
    <div class="brand">` + logo + func() string { if logo == "" { return `<div class="mark">` + htmlEscape(mark) + `</div>` }; return "" }() + `<div class="co">` + htmlEscape(brand.Name) + `<small>Rental Agreement</small></div></div>
    <div class="doc"><div class="kind">Agreement</div><div class="num">` + htmlEscape(booking) + `</div></div>
  </div>
  ` + func() string { if contactLine == "" { return "" }; return `<div class="contact-strip">` + contactLine + `</div>` }() + `
  <div class="title">` + htmlEscape(title) + `</div>
  <div class="body">` + bodyHTML.String() + `</div>
  <div class="sig">
    <div><div style="height:40px;"></div><div class="line">Renter signature &nbsp;·&nbsp; Date</div></div>
    <div><div style="height:40px;"></div><div class="line">Authorized agent — ` + htmlEscape(brand.Name) + `</div></div>
  </div>
  <div class="foot">` + htmlEscape(brand.Name) + ` &nbsp;·&nbsp; Agreement <span style="font-family:ui-monospace;">` + htmlEscape(booking) + `</span> &nbsp;·&nbsp; This document is computer-generated and valid without signature when accompanied by a payment confirmation.</div>
</div>
</body></html>`
}

// htmlEscape — minimal escape (text only, no attributes).
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
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
