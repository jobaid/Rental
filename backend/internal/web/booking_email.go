package web

import (
	"context"
	"fmt"
	"html"
	"log"
	"strings"

	"github.com/google/uuid"
)

// =============================================================================
// Booking confirmation email
//
// Called from BookPost after a successful booking. Builds a branded HTML
// email with every detail: vehicle, dates, cost breakdown, insurance,
// deposit, pickup location, portal login (if the customer set a password).
// Also logs the outbound message into the Communication Center.
// =============================================================================

type BookingConfirmPayload struct {
	ResID          uuid.UUID
	BookingNumber  string
	ToEmail        string
	CustomerName   string
	CustomerFirst  string
	CustomerID     uuid.UUID
	VehicleLabel   string
	PickupAt       string
	ReturnAt       string
	Days           int
	DailyRateCents int
	SubtotalCents  int
	InsuranceLabel string
	InsuranceCents int
	TaxLabel       string
	TaxRatePct     int
	TaxCents       int
	TotalCents     int
	DepositCents   int
	PortalPassword string // non-empty if a portal account was created
}

func (h *Handler) sendBookingConfirmation(ctx context.Context, cid uuid.UUID, p BookingConfirmPayload) {
	if p.ToEmail == "" {
		return
	}
	brand := h.loadBrand(ctx)
	if brand.Name == "" {
		brand.Name = "mosrental"
	}
	// Compose.
	htmlBody := h.buildBookingConfirmHTML(brand, p)
	textBody := buildBookingConfirmText(brand, p)
	subject := fmt.Sprintf("Booking confirmed · %s · %s", p.BookingNumber, brand.Name)

	m := h.mailerFor(ctx, cid)
	if !m.Cfg.Configured() {
		log.Printf("booking confirmation email: SMTP not configured; booking=%s", p.BookingNumber)
		// Still log the message in the Communication Center so the admin can resend manually.
		h.logQueuedBookingEmail(ctx, cid, p, subject, htmlBody, "SMTP not configured")
		return
	}
	providerID, err := m.SendHTML(p.ToEmail, subject, htmlBody, textBody)
	if err != nil {
		log.Printf("booking confirmation email failed: booking=%s err=%v", p.BookingNumber, err)
		h.logQueuedBookingEmail(ctx, cid, p, subject, htmlBody, err.Error())
		return
	}
	h.logSentBookingEmail(ctx, cid, p, subject, htmlBody, providerID)
}

// Thread the email into the Communication Center so staff can see it later.
func (h *Handler) logSentBookingEmail(ctx context.Context, cid uuid.UUID, p BookingConfirmPayload, subject, body, providerID string) {
	tid, _ := h.ensureThread(ctx, cid, &p.CustomerID, &p.ResID)
	_, _ = h.Pool.Exec(ctx, `
		INSERT INTO communication_messages
		  (thread_id, company_id, customer_id, reservation_id, sender_type, channel, direction,
		   recipient, subject, body, status, provider_message_id, sent_at)
		VALUES ($1,$2,$3,$4,'system','email','outbound',$5,$6,$7,'sent',NULLIF($8,''),NOW())
	`, tid, cid, p.CustomerID, p.ResID, p.ToEmail, subject, body, providerID)
}

func (h *Handler) logQueuedBookingEmail(ctx context.Context, cid uuid.UUID, p BookingConfirmPayload, subject, body, errMsg string) {
	tid, _ := h.ensureThread(ctx, cid, &p.CustomerID, &p.ResID)
	_, _ = h.Pool.Exec(ctx, `
		INSERT INTO communication_messages
		  (thread_id, company_id, customer_id, reservation_id, sender_type, channel, direction,
		   recipient, subject, body, status, error_message)
		VALUES ($1,$2,$3,$4,'system','email','outbound',$5,$6,$7,'queued',NULLIF($8,''))
	`, tid, cid, p.CustomerID, p.ResID, p.ToEmail, subject, body, errMsg)
}

// =============================================================================
// Rendering
// =============================================================================

func dollarsFmt(c int) string { return fmt.Sprintf("$%.2f", float64(c)/100) }

func (h *Handler) buildBookingConfirmHTML(brand Brand, p BookingConfirmPayload) string {
	esc := html.EscapeString
	logo := ""
	if brand.LogoURL != "" {
		src := brand.LogoURL
		if !strings.HasPrefix(src, "http") && h.PublicBase != "" {
			src = strings.TrimRight(h.PublicBase, "/") + src
		}
		logo = `<span style="display:inline-flex;align-items:center;justify-content:center;background:#0a0a0a;padding:8px 14px;border-radius:8px;border:2px solid #dc2626;"><img src="` + esc(src) + `" alt="` + esc(brand.Name) + `" style="max-height:48px;max-width:160px;object-fit:contain;"></span>`
	}

	portalBlock := ""
	if p.PortalPassword != "" && h.PublicBase != "" {
		portalBlock = `
<div style="margin-top:24px;padding:18px 20px;background:#0a0a0a;color:#fff;border-radius:10px;border-left:4px solid #dc2626;">
  <div style="font-size:11px;letter-spacing:2px;text-transform:uppercase;color:#f59e0b;font-weight:700;margin-bottom:6px;">Your Portal Account</div>
  <div style="font-size:15px;font-weight:600;margin-bottom:8px;">Manage your booking anytime</div>
  <table cellpadding="0" cellspacing="0" border="0" style="width:100%;font-size:13.5px;">
    <tr><td style="color:#9ca3af;padding:3px 0;">Login URL</td><td style="color:#fff;"><a href="` + esc(h.PublicBase) + `/portal/login" style="color:#fca5a5;">` + esc(h.PublicBase) + `/portal/login</a></td></tr>
    <tr><td style="color:#9ca3af;padding:3px 0;">Email</td><td style="color:#fff;font-family:monospace;">` + esc(p.ToEmail) + `</td></tr>
    <tr><td style="color:#9ca3af;padding:3px 0;">Password</td><td style="color:#fff;font-family:monospace;letter-spacing:.5px;">` + esc(p.PortalPassword) + `</td></tr>
  </table>
  <p style="margin:10px 0 0;font-size:11.5px;color:#9ca3af;">Keep this password private. You can change it after signing in.</p>
</div>`
	} else if h.PublicBase != "" {
		portalBlock = `
<div style="margin-top:24px;padding:14px 18px;background:#f9fafb;border:1px solid #e5e7eb;border-radius:10px;font-size:13px;">
  No portal account yet. <a href="` + esc(h.PublicBase) + `/portal/login" style="color:#dc2626;font-weight:600;">Set a password</a> to view your bookings online.
</div>`
	}

	insuranceLine := ""
	if p.InsuranceCents > 0 {
		insuranceLine = `<tr><td style="padding:6px 0;color:#6b7280;">Insurance — ` + esc(p.InsuranceLabel) + `</td><td style="padding:6px 0;text-align:right;color:#0a0a0a;font-variant-numeric:tabular-nums;">` + dollarsFmt(p.InsuranceCents) + `</td></tr>`
	}

	contactLine := ""
	parts := []string{}
	if brand.Phone != "" {
		parts = append(parts, `📞 <a href="tel:`+esc(brand.Phone)+`" style="color:#fff;text-decoration:none;">`+esc(brand.Phone)+`</a>`)
	}
	if brand.Email != "" {
		parts = append(parts, `✉ <a href="mailto:`+esc(brand.Email)+`" style="color:#fff;text-decoration:none;">`+esc(brand.Email)+`</a>`)
	}
	if brand.Hours != "" {
		parts = append(parts, `🕒 `+esc(brand.Hours))
	}
	if brand.Address != "" {
		parts = append(parts, `📍 `+esc(brand.Address))
	}
	contactLine = strings.Join(parts, " &nbsp;·&nbsp; ")

	return `<!doctype html><html><head><meta charset="utf-8"><title>Booking ` + esc(p.BookingNumber) + `</title></head>
<body style="margin:0;background:#eceef2;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Arial,sans-serif;color:#0a0a0a;">
<div style="max-width:620px;margin:24px auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 10px 30px rgba(0,0,0,.08);">
  <!-- Band -->
  <div style="background:#0a0a0a;color:#fff;padding:22px 28px;border-bottom:4px solid #dc2626;display:flex;align-items:center;justify-content:space-between;">
    <div>` + logo + `</div>
    <div style="text-align:right;">
      <div style="font-size:10.5px;letter-spacing:3px;text-transform:uppercase;opacity:.8;">Booking confirmed</div>
      <div style="font-family:monospace;font-size:16px;font-weight:700;color:#dc2626;">` + esc(p.BookingNumber) + `</div>
    </div>
  </div>

  <!-- Body -->
  <div style="padding:28px;">
    <h2 style="margin:0 0 6px;font-size:22px;color:#0a0a0a;">Hi ` + esc(p.CustomerFirst) + `, your car is booked.</h2>
    <p style="margin:0 0 18px;color:#4b5563;font-size:14px;">Thanks for choosing ` + esc(brand.Name) + `. Here's everything about your reservation.</p>

    <!-- Rental block -->
    <table cellpadding="0" cellspacing="0" border="0" style="width:100%;border-top:1px solid #e5e7eb;border-bottom:1px solid #e5e7eb;margin-bottom:18px;">
      <tr><td style="padding:10px 0;color:#6b7280;font-size:12.5px;width:40%;">Vehicle</td><td style="padding:10px 0;font-size:14px;font-weight:600;">` + esc(p.VehicleLabel) + `</td></tr>
      <tr><td style="padding:6px 0;color:#6b7280;font-size:12.5px;">Pickup</td><td style="padding:6px 0;font-size:14px;">` + esc(p.PickupAt) + `</td></tr>
      <tr><td style="padding:6px 0;color:#6b7280;font-size:12.5px;">Return</td><td style="padding:6px 0;font-size:14px;">` + esc(p.ReturnAt) + `</td></tr>
      <tr><td style="padding:6px 0 10px;color:#6b7280;font-size:12.5px;">Duration</td><td style="padding:6px 0 10px;font-size:14px;">` + fmt.Sprintf("%d", p.Days) + ` day(s)</td></tr>
    </table>

    <!-- Cost -->
    <div style="font-size:11px;letter-spacing:2px;color:#dc2626;text-transform:uppercase;font-weight:700;margin-bottom:8px;">Charges</div>
    <table cellpadding="0" cellspacing="0" border="0" style="width:100%;font-size:13.5px;">
      <tr><td style="padding:6px 0;color:#6b7280;">Rental (` + fmt.Sprintf("%d", p.Days) + ` × ` + dollarsFmt(p.DailyRateCents) + `)</td><td style="padding:6px 0;text-align:right;font-variant-numeric:tabular-nums;">` + dollarsFmt(p.SubtotalCents) + `</td></tr>
      ` + insuranceLine + `
      <tr><td style="padding:6px 0;color:#6b7280;">` + esc(p.TaxLabel) + ` (` + fmt.Sprintf("%d", p.TaxRatePct) + `%)</td><td style="padding:6px 0;text-align:right;font-variant-numeric:tabular-nums;">` + dollarsFmt(p.TaxCents) + `</td></tr>
      <tr><td style="padding:12px 0 6px;font-size:16px;font-weight:800;border-top:2px solid #0a0a0a;">Total</td><td style="padding:12px 0 6px;text-align:right;font-size:16px;font-weight:800;border-top:2px solid #0a0a0a;font-variant-numeric:tabular-nums;">` + dollarsFmt(p.TotalCents) + `</td></tr>
    </table>
    ` + func() string {
		if p.DepositCents > 0 {
			return `<div style="margin-top:14px;padding:10px 14px;background:#fef3c7;border-left:3px solid #f59e0b;border-radius:6px;font-size:13px;color:#78350f;"><strong>+ Security deposit:</strong> ` + dollarsFmt(p.DepositCents) + ` refundable, held at pickup via card.</div>`
		}
		return ""
	}() + portalBlock + `

    <p style="margin:24px 0 0;font-size:12.5px;color:#6b7280;">Need to make changes or cancel? Reply to this email or contact ` + esc(brand.Name) + ` with your booking number <strong>` + esc(p.BookingNumber) + `</strong>.</p>
  </div>

  <!-- Footer -->
  <div style="background:#0a0a0a;color:#9ca3af;padding:16px 28px;font-size:12px;text-align:center;border-top:4px solid #dc2626;">
    ` + func() string { if contactLine != "" { return contactLine }; return esc(brand.Name) }() + `
  </div>
</div>
</body></html>`
}

func buildBookingConfirmText(brand Brand, p BookingConfirmPayload) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Hi %s,\n\nYour booking is confirmed. Here are the details:\n\n", p.CustomerFirst)
	fmt.Fprintf(&b, "Booking number: %s\n", p.BookingNumber)
	fmt.Fprintf(&b, "Vehicle:        %s\n", p.VehicleLabel)
	fmt.Fprintf(&b, "Pickup:         %s\n", p.PickupAt)
	fmt.Fprintf(&b, "Return:         %s\n", p.ReturnAt)
	fmt.Fprintf(&b, "Duration:       %d day(s)\n\n", p.Days)
	fmt.Fprintf(&b, "Rental (%d × %s):  %s\n", p.Days, dollarsFmt(p.DailyRateCents), dollarsFmt(p.SubtotalCents))
	if p.InsuranceCents > 0 {
		fmt.Fprintf(&b, "Insurance — %s:  %s\n", p.InsuranceLabel, dollarsFmt(p.InsuranceCents))
	}
	fmt.Fprintf(&b, "%s (%d%%):          %s\n", p.TaxLabel, p.TaxRatePct, dollarsFmt(p.TaxCents))
	fmt.Fprintf(&b, "TOTAL:              %s\n", dollarsFmt(p.TotalCents))
	if p.DepositCents > 0 {
		fmt.Fprintf(&b, "+ Refundable security deposit (held at pickup): %s\n", dollarsFmt(p.DepositCents))
	}
	if p.PortalPassword != "" && brand.Name != "" {
		b.WriteString("\n-----\n")
		b.WriteString("PORTAL ACCOUNT\n")
		b.WriteString("Login:    " + brand.Name + " customer portal\n")
		b.WriteString("Email:    " + p.ToEmail + "\n")
		b.WriteString("Password: " + p.PortalPassword + "\n")
		b.WriteString("Keep this password private. You can change it after signing in.\n")
	}
	b.WriteString("\n-----\n")
	if brand.Phone != "" {
		b.WriteString("Phone: " + brand.Phone + "\n")
	}
	if brand.Email != "" {
		b.WriteString("Email: " + brand.Email + "\n")
	}
	if brand.Address != "" {
		b.WriteString("Address: " + brand.Address + "\n")
	}
	b.WriteString("\n— " + brand.Name + "\n")
	return b.String()
}
