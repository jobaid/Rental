package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// =============================================================================
// Legal / Agreements — editable templates
//
// Three editable blobs live in the settings table under separate keys:
//
//   key: 'agreement'           → the rental agreement served as PDF
//   key: 'terms_and_conditions' → general T&C seeded onto new invoices
//   key: 'insurance_terms'     → insurance-specific T&C (already used by
//                                 invoice renderer when customer took cover)
//
// Each row stores JSON like: {"title": "...", "body": "..."}. The body is
// plain text with placeholders that get expanded at render time.
// =============================================================================

// Placeholder tokens the templates understand.
var legalPlaceholders = []struct {
	Token string
	Label string
}{
	{"{{COMPANY}}", "Company name"},
	{"{{COMPANY_ADDRESS}}", "Company address"},
	{"{{BOOKING}}", "Booking number"},
	{"{{STATUS}}", "Booking status"},
	{"{{CUSTOMER}}", "Customer full name"},
	{"{{CUSTOMER_EMAIL}}", "Customer email"},
	{"{{VEHICLE}}", "Vehicle year make model"},
	{"{{PLATE}}", "License plate"},
	{"{{PICKUP}}", "Pickup date & time"},
	{"{{RETURN}}", "Return date & time"},
	{"{{DAYS}}", "Rental days"},
	{"{{DAILY_RATE}}", "Daily rate (formatted)"},
	{"{{TOTAL}}", "Total (formatted)"},
	{"{{DATE}}", "Today's date"},
}

// LegalTemplate is the stored form of a single template.
type LegalTemplate struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Defaults — used when the DB row doesn't exist yet.
var defaultAgreement = LegalTemplate{
	Title: "RENTAL AGREEMENT",
	Body: `This Rental Agreement is entered into on {{DATE}} between {{COMPANY}} ("Owner") and {{CUSTOMER}} ("Renter") for the rental of the following vehicle:

Vehicle:  {{VEHICLE}} ({{PLATE}})
Booking:  {{BOOKING}}
Pickup:   {{PICKUP}}
Return:   {{RETURN}}
Days:     {{DAYS}}
Rate:     {{DAILY_RATE}} / day
Total:    {{TOTAL}}

1. CONDITION & USE. The Renter acknowledges receipt of the vehicle in good working order and agrees to return it in the same condition, normal wear and tear excepted.

2. AUTHORIZED DRIVERS. Only the Renter and additional drivers listed on this agreement may operate the vehicle. All drivers must hold a valid driver's license.

3. PROHIBITED USE. The vehicle shall not be used: to transport persons or property for hire; to push or tow another vehicle; off paved roads; in any race or speed test; while the driver is under the influence of alcohol, drugs, or any other substance that impairs driving ability; or in violation of any law.

4. FUEL. The vehicle must be returned with the same fuel level it was issued at. A refuelling charge plus a service fee applies when it is not.

5. TOLLS & TRAFFIC FINES. The Renter is responsible for all tolls, parking charges, moving violations and administrative fees incurred during the rental, plus a per-transaction processing fee.

6. DAMAGE. The Renter is responsible for all damage to the vehicle during the rental, up to the applicable deductible if insurance was taken.

7. LATE RETURN. A per-hour late fee applies for returns beyond the agreed return time.

8. GOVERNING LAW. This agreement is governed by the laws of the jurisdiction of the issuing branch.

Signed on behalf of {{COMPANY}}:  __________________________

Signed by {{CUSTOMER}}:            __________________________`,
}

var defaultTerms = LegalTemplate{
	Title: "TERMS & CONDITIONS",
	Body: `By completing this rental, {{CUSTOMER}} accepts the following terms:

• Payment is due on or before the vehicle pickup. Outstanding balances are collected via the payment method on file.
• Cancellations made more than 24 hours before pickup are fully refundable. Cancellations within 24 hours are subject to a one-day charge.
• The Renter is responsible for all tolls, traffic fines, and parking violations, plus a reasonable administration fee.
• The vehicle must be returned clean and with the fuel level at pickup. Cleaning and refuelling fees apply if not.
• Smoking is prohibited in all vehicles; a penalty applies to any vehicle returned with smoke odor.
• The Renter authorizes {{COMPANY}} to charge the payment method on file for any charges that accrue after return, including delayed tolls and damage.

Questions? Reply to this invoice or contact {{COMPANY}}.`,
}

var defaultInsurance = LegalTemplate{
	Title: "INSURANCE — TERMS & CONDITIONS",
	Body: `By accepting optional insurance coverage, the Renter agrees:

1. COVERAGE. Covers damage to the rental vehicle resulting from collision with another vehicle or object, subject to the deductible on the policy you selected.

2. DEDUCTIBLE. The Renter remains responsible for the first portion of any claim as stated on the invoice.

3. EXCLUSIONS. Coverage does not apply to damage caused by: driving under the influence; unauthorized drivers; off-road use; wilful negligence; theft resulting from unlocked / unattended vehicle; tyre, undercarriage, and interior stains or odours; loss of personal property.

4. CLAIMS. All incidents must be reported to {{COMPANY}} within 24 hours and accompanied by a police report where applicable. Failure to report voids coverage.

5. GOVERNING LAW. These terms are governed by the laws of the jurisdiction of the issuing branch.`,
}

// legalGet loads a template by key with defaults.
func (h *Handler) legalGet(ctx context.Context, cid uuid.UUID, key string, def LegalTemplate) LegalTemplate {
	var raw []byte
	err := h.Pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE company_id=$1 AND key=$2`, cid, key).Scan(&raw)
	if err != nil {
		return def
	}
	var t LegalTemplate
	if err := json.Unmarshal(raw, &t); err != nil {
		return def
	}
	if strings.TrimSpace(t.Body) == "" {
		return def
	}
	return t
}

func (h *Handler) legalSave(ctx context.Context, cid uuid.UUID, key string, t LegalTemplate) error {
	b, _ := json.Marshal(t)
	_, err := h.Pool.Exec(ctx, `
		INSERT INTO settings (company_id, key, value) VALUES ($1,$2,$3::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, key, string(b))
	return err
}

// expandLegal substitutes placeholder tokens for a specific reservation.
func (h *Handler) expandLegal(ctx context.Context, cid uuid.UUID, body string, resID uuid.UUID) string {
	// Pull everything in one row.
	var (
		company, companyAddr                          string
		booking, status, vehicle, plate, customerName string
		customerEmail                                 string
		pickup, ret                                   time.Time
		dailyRate, days, total                        int
	)
	err := h.Pool.QueryRow(ctx, `
		SELECT co.name,
		       COALESCE((SELECT address FROM branches WHERE company_id=co.id AND is_active ORDER BY created_at LIMIT 1), ''),
		       r.booking_number, r.status,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       COALESCE(v.license_plate, ''),
		       COALESCE(cu.first_name||' '||cu.last_name, ''),
		       COALESCE(cu.email, ''),
		       r.pickup_at, r.return_at,
		       r.daily_rate_cents, r.rental_days, r.total_cents
		FROM reservations r
		JOIN companies co ON co.id=r.company_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, resID).Scan(&company, &companyAddr, &booking, &status, &vehicle, &plate,
		&customerName, &customerEmail, &pickup, &ret, &dailyRate, &days, &total)
	if err != nil {
		return body
	}
	r := strings.NewReplacer(
		"{{COMPANY}}", company,
		"{{COMPANY_ADDRESS}}", companyAddr,
		"{{BOOKING}}", booking,
		"{{STATUS}}", status,
		"{{CUSTOMER}}", customerName,
		"{{CUSTOMER_EMAIL}}", customerEmail,
		"{{VEHICLE}}", strings.TrimSpace(vehicle),
		"{{PLATE}}", plate,
		"{{PICKUP}}", pickup.Format("Mon 02 Jan 2006 · 15:04"),
		"{{RETURN}}", ret.Format("Mon 02 Jan 2006 · 15:04"),
		"{{DAYS}}", fmt.Sprintf("%d", days),
		"{{DAILY_RATE}}", fmt.Sprintf("$%.2f", float64(dailyRate)/100),
		"{{TOTAL}}", fmt.Sprintf("$%.2f", float64(total)/100),
		"{{DATE}}", time.Now().Format("Mon 02 Jan 2006"),
	)
	return r.Replace(body)
}

// =============================================================================
// HTTP handlers
// =============================================================================

// LegalSettings renders the edit page.
func (h *Handler) LegalSettings(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	agreement := h.legalGet(r.Context(), cid, "agreement", defaultAgreement)
	terms := h.legalGet(r.Context(), cid, "terms_and_conditions", defaultTerms)
	// insurance_terms migrated from older code; same shape.
	insurance := h.legalGetInsurance(r.Context(), cid)

	h.render(w, r, "admin/legal.html", "Agreements & terms", "settings", map[string]any{
		"Agreement":    agreement,
		"Terms":        terms,
		"Insurance":    insurance,
		"Placeholders": legalPlaceholders,
		"Flash":        r.URL.Query().Get("flash"),
		"Err":          r.URL.Query().Get("err"),
	})
}

// The older `insurance_terms` key may have been stored as `{"text": "..."}`
// (per the insuranceTerms() reader). Handle both shapes transparently.
func (h *Handler) legalGetInsurance(ctx context.Context, cid uuid.UUID) LegalTemplate {
	var raw []byte
	err := h.Pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE company_id=$1 AND key='insurance_terms'`, cid).Scan(&raw)
	if err != nil {
		return defaultInsurance
	}
	var t LegalTemplate
	if err := json.Unmarshal(raw, &t); err == nil && strings.TrimSpace(t.Body) != "" {
		return t
	}
	// Legacy shape: {"text": "..."}
	var legacy struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &legacy); err == nil && strings.TrimSpace(legacy.Text) != "" {
		return LegalTemplate{Title: defaultInsurance.Title, Body: legacy.Text}
	}
	return defaultInsurance
}

// LegalSave accepts one of three forms (`kind` field in the POST body).
func (h *Handler) LegalSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	kind := r.FormValue("kind")
	t := LegalTemplate{
		Title: strings.TrimSpace(r.FormValue("title")),
		Body:  r.FormValue("body"),
	}
	if t.Title == "" {
		t.Title = "Document"
	}
	key := ""
	switch kind {
	case "agreement":
		key = "agreement"
	case "terms":
		key = "terms_and_conditions"
	case "insurance":
		key = "insurance_terms"
	default:
		http.Redirect(w, r, "/admin/settings/legal?err=Unknown+template", http.StatusSeeOther)
		return
	}
	if err := h.legalSave(r.Context(), cid, key, t); err != nil {
		http.Redirect(w, r, "/admin/settings/legal?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings/legal?flash=Saved#"+kind, http.StatusSeeOther)
}

// LegalReset restores a template to its built-in default.
func (h *Handler) LegalReset(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	kind := r.FormValue("kind")
	keyAndDefault := map[string]struct {
		key string
		def LegalTemplate
	}{
		"agreement": {"agreement", defaultAgreement},
		"terms":     {"terms_and_conditions", defaultTerms},
		"insurance": {"insurance_terms", defaultInsurance},
	}
	kd, ok := keyAndDefault[kind]
	if !ok {
		http.Redirect(w, r, "/admin/settings/legal?err=Unknown+template", http.StatusSeeOther)
		return
	}
	if err := h.legalSave(r.Context(), cid, kd.key, kd.def); err != nil {
		http.Redirect(w, r, "/admin/settings/legal?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings/legal?flash=Restored+default#"+kind, http.StatusSeeOther)
}
