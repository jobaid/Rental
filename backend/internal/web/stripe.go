package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// =============================================================================
// STRIPE — online payments for invoices.
//
// Integration shape:
//   - Stripe Checkout (hosted page) for one-off invoice payments. Simpler +
//     fewer PCI considerations than embedded Payment Elements.
//   - Raw HTTPS to api.stripe.com (no SDK dependency to drag in).
//   - Webhook verification done manually against t=<timestamp>,v1=<sig> using
//     HMAC-SHA256 with the stored webhook signing secret.
//   - Idempotent event processing via stripe_events table + unique
//     constraint on provider_event_id.
//   - Secret key stored in settings (key='stripe') encrypted-at-rest by the
//     database only (not in code). Env vars STRIPE_SECRET_KEY etc. override.
// =============================================================================

type StripeConfig struct {
	Mode             string // test | live
	PublishableKey   string
	SecretKey        string
	WebhookSecret    string
	HasSecret        bool // true = DB holds a secret (not shown in UI)
	HasWebhookSecret bool
	Configured       bool
}

// stripeCfg reads merged config: env overrides anything in the DB.
func (h *Handler) stripeCfg(ctx context.Context, cid uuid.UUID) StripeConfig {
	cfg := StripeConfig{Mode: "test"}
	var raw []byte
	if err := h.Pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE company_id=$1 AND key='stripe'`, cid).Scan(&raw); err == nil {
		var m map[string]string
		if json.Unmarshal(raw, &m) == nil {
			if v := m["mode"]; v != "" {
				cfg.Mode = v
			}
			cfg.PublishableKey = m["publishableKey"]
			cfg.SecretKey = m["secretKey"]
			cfg.WebhookSecret = m["webhookSecret"]
		}
	}
	// Env override wins.
	if v := os.Getenv("STRIPE_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := os.Getenv("STRIPE_PUBLISHABLE_KEY"); v != "" {
		cfg.PublishableKey = v
	}
	if v := os.Getenv("STRIPE_SECRET_KEY"); v != "" {
		cfg.SecretKey = v
	}
	if v := os.Getenv("STRIPE_WEBHOOK_SECRET"); v != "" {
		cfg.WebhookSecret = v
	}
	cfg.HasSecret = cfg.SecretKey != ""
	cfg.HasWebhookSecret = cfg.WebhookSecret != ""
	cfg.Configured = cfg.PublishableKey != "" && cfg.SecretKey != ""
	return cfg
}

// =============================================================================
// Settings page
// =============================================================================

func (h *Handler) StripeSettings(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	cfg := h.stripeCfg(r.Context(), cid)
	// Never leak the secret back to the browser.
	hidden := cfg
	hidden.SecretKey = ""
	hidden.WebhookSecret = ""
	h.render(w, r, "admin/stripe.html", "Stripe", "settings", map[string]any{
		"Cfg":   hidden,
		"Flash": r.URL.Query().Get("flash"),
		"Err":   r.URL.Query().Get("err"),
	})
}

// StripeSettingsSave accepts the four fields. Empty secret / webhook secret
// means "keep the current value".
func (h *Handler) StripeSettingsSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	cur := h.stripeCfg(r.Context(), cid)

	mode := strings.TrimSpace(r.FormValue("mode"))
	if mode != "test" && mode != "live" {
		mode = "test"
	}
	pub := strings.TrimSpace(r.FormValue("publishableKey"))
	sec := strings.TrimSpace(r.FormValue("secretKey"))
	wh := strings.TrimSpace(r.FormValue("webhookSecret"))
	if sec == "" {
		sec = cur.SecretKey
	}
	if wh == "" {
		wh = cur.WebhookSecret
	}

	body, _ := json.Marshal(map[string]string{
		"mode":           mode,
		"publishableKey": pub,
		"secretKey":      sec,
		"webhookSecret":  wh,
	})
	if _, err := h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'stripe',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(body)); err != nil {
		http.Redirect(w, r, "/admin/settings/stripe?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings/stripe?flash=Saved", http.StatusSeeOther)
}

// StripeTest calls Stripe to list balance; cheapest round-trip to verify
// credentials without making a real charge.
func (h *Handler) StripeTest(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	cfg := h.stripeCfg(r.Context(), cid)
	if !cfg.Configured {
		http.Redirect(w, r, "/admin/settings/stripe?err=Set+keys+first", http.StatusSeeOther)
		return
	}
	req, _ := http.NewRequest("GET", "https://api.stripe.com/v1/balance", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.SecretKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Redirect(w, r, "/admin/settings/stripe?err="+err.Error(), http.StatusSeeOther)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		http.Redirect(w, r, "/admin/settings/stripe?err=Stripe+rejected%3A+"+string(b)[:min(len(b), 100)], http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings/stripe?flash=Stripe+connection+OK", http.StatusSeeOther)
}

// =============================================================================
// Create Checkout Session for an invoice
// =============================================================================

// stripeCreateSession hits api.stripe.com directly. Returns (session_id, url).
func (h *Handler) stripeCreateSession(cfg StripeConfig, args url.Values) (string, string, error) {
	req, err := http.NewRequest("POST", "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(args.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.SecretKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Idempotency so a double-click from the admin doesn't create two sessions
	// (Stripe caches the response for 24h against this key).
	req.Header.Set("Idempotency-Key", args.Get("client_reference_id")+"-"+time.Now().UTC().Format("20060102150405"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("stripe %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.URL, nil
}

// InvoicePaymentLink is the admin-side action: create a Checkout Session for
// the invoice's outstanding balance, store the session id on a pending
// payments row, and redirect back with the URL ready to send.
func (h *Handler) InvoicePaymentLink(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	idStr := chi.URLParam(r, "id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cfg := h.stripeCfg(r.Context(), cid)
	if !cfg.Configured {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err=Configure+Stripe+first", http.StatusSeeOther)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if inv.TotalCents <= 0 {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err=Invoice+total+is+zero", http.StatusSeeOther)
		return
	}
	base := h.shareBase(r)
	args := url.Values{}
	args.Set("mode", "payment")
	args.Set("payment_method_types[]", "card")
	args.Set("line_items[0][quantity]", "1")
	args.Set("line_items[0][price_data][currency]", "usd")
	args.Set("line_items[0][price_data][product_data][name]",
		fmt.Sprintf("%s %s · Booking %s", inv.KindLabel(), inv.Number, inv.BookingNumber))
	args.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(inv.TotalCents))
	args.Set("success_url", base+"/payments/success?inv="+inv.ID.String())
	args.Set("cancel_url", base+"/payments/cancel?inv="+inv.ID.String())
	args.Set("client_reference_id", inv.ID.String())
	args.Set("metadata[invoice_id]", inv.ID.String())
	args.Set("metadata[company_id]", cid.String())
	args.Set("metadata[reservation_id]", inv.ReservationID.String())
	if inv.CustomerEmail != "" {
		args.Set("customer_email", inv.CustomerEmail)
	}

	sessID, sessURL, err := h.stripeCreateSession(cfg, args)
	if err != nil {
		http.Redirect(w, r, "/admin/invoices/"+idStr+"?err=Stripe%3A+"+err.Error(), http.StatusSeeOther)
		return
	}

	// Record a pending payments row so admins can see the attempt.
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO payments (company_id, reservation_id, invoice_id, customer_id, amount_cents, method, status,
		                      stripe_session_id, stripe_mode, reference)
		VALUES ($1,$2,$3,NULL,$4,'stripe','pending',$5,$6,$7)
		ON CONFLICT (stripe_session_id) DO NOTHING
	`, cid, inv.ReservationID, inv.ID, inv.TotalCents, sessID, cfg.Mode, sessURL)

	flash := url.QueryEscape("Payment link ready: " + sessURL)
	http.Redirect(w, r, "/admin/invoices/"+idStr+"?flash="+flash+"&paylink="+url.QueryEscape(sessURL), http.StatusSeeOther)
}

// =============================================================================
// Customer-facing: Pay this invoice (from the public invoice token or portal)
// =============================================================================

// PublicInvoicePay is the "Pay now" button on the public invoice token page.
// Opens a Checkout Session and redirects straight to Stripe.
func (h *Handler) PublicInvoicePay(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var invID, cid uuid.UUID
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT id, company_id FROM invoices WHERE public_token=$1`, tok).Scan(&invID, &cid); err != nil {
		http.NotFound(w, r)
		return
	}
	cfg := h.stripeCfg(r.Context(), cid)
	if !cfg.Configured {
		http.Error(w, "payments not configured", 503)
		return
	}
	inv, err := h.loadInvoice(r.Context(), cid, invID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if inv.TotalCents <= 0 {
		http.Error(w, "invoice total is zero", 400)
		return
	}
	base := h.shareBase(r)
	args := url.Values{}
	args.Set("mode", "payment")
	args.Set("payment_method_types[]", "card")
	args.Set("line_items[0][quantity]", "1")
	args.Set("line_items[0][price_data][currency]", "usd")
	args.Set("line_items[0][price_data][product_data][name]",
		fmt.Sprintf("%s %s · Booking %s", inv.KindLabel(), inv.Number, inv.BookingNumber))
	args.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(inv.TotalCents))
	args.Set("success_url", base+"/payments/success?inv="+inv.ID.String())
	args.Set("cancel_url", base+"/invoices/"+tok)
	args.Set("client_reference_id", inv.ID.String())
	args.Set("metadata[invoice_id]", inv.ID.String())
	args.Set("metadata[company_id]", cid.String())
	args.Set("metadata[reservation_id]", inv.ReservationID.String())
	if inv.CustomerEmail != "" {
		args.Set("customer_email", inv.CustomerEmail)
	}
	sessID, sessURL, err := h.stripeCreateSession(cfg, args)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO payments (company_id, reservation_id, invoice_id, customer_id, amount_cents, method, status,
		                      stripe_session_id, stripe_mode, reference)
		VALUES ($1,$2,$3,NULL,$4,'stripe','pending',$5,$6,$7)
		ON CONFLICT (stripe_session_id) DO NOTHING
	`, cid, inv.ReservationID, inv.ID, inv.TotalCents, sessID, cfg.Mode, sessURL)
	http.Redirect(w, r, sessURL, http.StatusSeeOther)
}

// PaymentSuccess + PaymentCancel are the browser-redirect landing pages.
// They do NOT mark the invoice paid — only the webhook does.
func (h *Handler) PaymentSuccess(w http.ResponseWriter, r *http.Request) {
	invStr := r.URL.Query().Get("inv")
	var token, booking string
	var invTotal int
	_ = h.Pool.QueryRow(r.Context(), `
		SELECT i.public_token, r.booking_number, i.total_cents
		  FROM invoices i JOIN reservations r ON r.id=i.reservation_id
		 WHERE i.id=$1
	`, invStr).Scan(&token, &booking, &invTotal)
	h.render(w, r, "public/payment_success.html", "Thank you", "", map[string]any{
		"BookingNumber": booking,
		"Token":         token,
		"TotalCents":    invTotal,
	})
}

func (h *Handler) PaymentCancel(w http.ResponseWriter, r *http.Request) {
	invStr := r.URL.Query().Get("inv")
	var token string
	_ = h.Pool.QueryRow(r.Context(), `SELECT public_token FROM invoices WHERE id=$1`, invStr).Scan(&token)
	h.render(w, r, "public/payment_cancel.html", "Payment cancelled", "", map[string]any{
		"Token": token,
	})
}

// =============================================================================
// Webhook — the only place that marks invoices paid.
// =============================================================================

// StripeWebhook verifies the signature, stores the event idempotently, and
// applies the right action for the event type.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", 400)
		return
	}
	sigHeader := r.Header.Get("Stripe-Signature")

	// We don't know the company yet — try every company's config and pick the
	// one whose webhook secret validates the signature. In practice this is
	// one row so the loop is cheap.
	rows, err := h.Pool.Query(r.Context(),
		`SELECT company_id, value FROM settings WHERE key='stripe'`)
	if err != nil {
		http.Error(w, "db", 500)
		return
	}
	defer rows.Close()
	var cid uuid.UUID
	var secret string
	for rows.Next() {
		var c uuid.UUID
		var raw []byte
		if err := rows.Scan(&c, &raw); err != nil {
			continue
		}
		var m map[string]string
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		s := m["webhookSecret"]
		if envSec := os.Getenv("STRIPE_WEBHOOK_SECRET"); envSec != "" {
			s = envSec
		}
		if s == "" {
			continue
		}
		if verifyStripeSignature(sigHeader, body, s, 5*time.Minute) {
			cid, secret = c, s
			break
		}
	}
	rows.Close()
	if secret == "" {
		http.Error(w, "signature verification failed", 400)
		return
	}

	// Parse the envelope.
	var evt struct {
		ID       string          `json:"id"`
		Type     string          `json:"type"`
		Livemode bool            `json:"livemode"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, "parse", 400)
		return
	}

	// Idempotent insert.
	var eid uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `
		INSERT INTO stripe_events (company_id, provider_event_id, event_type, livemode, raw_payload)
		VALUES ($1,$2,$3,$4,$5::jsonb)
		ON CONFLICT (provider_event_id) DO NOTHING
		RETURNING id
	`, cid, evt.ID, evt.Type, evt.Livemode, string(body)).Scan(&eid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, err.Error(), 500)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Duplicate delivery — already processed.
		w.WriteHeader(200)
		return
	}

	// Handle supported event types.
	herr := h.handleStripeEvent(r.Context(), cid, evt.Type, evt.Data)
	if herr != nil {
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE stripe_events SET error_message=$1 WHERE id=$2`, herr.Error(), eid)
		http.Error(w, herr.Error(), 500)
		return
	}
	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE stripe_events SET processed_at=NOW() WHERE id=$1`, eid)
	w.WriteHeader(200)
}

func (h *Handler) handleStripeEvent(ctx context.Context, cid uuid.UUID, kind string, data json.RawMessage) error {
	switch kind {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		return h.markSessionPaid(ctx, cid, data)
	case "checkout.session.async_payment_failed", "checkout.session.expired":
		return h.markSessionFailed(ctx, cid, data)
	case "payment_intent.succeeded":
		// Fired after checkout.session.completed; no-op to avoid double accounting.
		return nil
	case "charge.refunded", "refund.updated":
		return h.markRefund(ctx, cid, data)
	}
	return nil // Unhandled event types are ignored (and marked processed).
}

type stripeSession struct {
	Object struct {
		ID              string `json:"id"`
		AmountTotal     int    `json:"amount_total"`
		PaymentIntent   string `json:"payment_intent"`
		PaymentStatus   string `json:"payment_status"`
		ClientReference string `json:"client_reference_id"`
		Metadata        struct {
			InvoiceID     string `json:"invoice_id"`
			ReservationID string `json:"reservation_id"`
			Purpose       string `json:"purpose"`
		} `json:"metadata"`
	} `json:"object"`
}

func (h *Handler) markSessionPaid(ctx context.Context, cid uuid.UUID, data json.RawMessage) error {
	var s stripeSession
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	sess := s.Object

	// Deposit charges never touch the invoice — they're a separate payments row.
	if sess.Metadata.Purpose == "security_deposit" {
		_, _ = h.Pool.Exec(ctx, `
			INSERT INTO payments (company_id, reservation_id, amount_cents, method, status,
			                      stripe_session_id, stripe_payment_intent, reference)
			VALUES ($1,$2::uuid,$3,'stripe','succeeded',$4,NULLIF($5,''),'security_deposit')
			ON CONFLICT (stripe_session_id) DO UPDATE
			  SET status='succeeded', stripe_payment_intent=COALESCE(EXCLUDED.stripe_payment_intent, payments.stripe_payment_intent)
		`, cid, nullIfEmpty(sess.Metadata.ReservationID), sess.AmountTotal, sess.ID, sess.PaymentIntent)
		_, _ = h.Pool.Exec(ctx,
			`UPDATE reservations SET deposit_charged_at=NOW() WHERE company_id=$1 AND id=$2`,
			cid, sess.Metadata.ReservationID)
		return nil
	}

	invID := sess.Metadata.InvoiceID
	if invID == "" {
		invID = sess.ClientReference
	}
	if invID == "" {
		return errors.New("no invoice id on session")
	}
	// Upsert the payments row: pending → succeeded.
	_, err := h.Pool.Exec(ctx, `
		INSERT INTO payments (company_id, reservation_id, invoice_id, amount_cents, method, status,
		                      stripe_session_id, stripe_payment_intent)
		VALUES ($1,$2::uuid,$3::uuid,$4,'stripe','succeeded',$5,NULLIF($6,''))
		ON CONFLICT (stripe_session_id) DO UPDATE
		  SET status='succeeded', stripe_payment_intent=COALESCE(EXCLUDED.stripe_payment_intent, payments.stripe_payment_intent)
	`, cid, nullIfEmpty(sess.Metadata.ReservationID), invID, sess.AmountTotal, sess.ID, sess.PaymentIntent)
	if err != nil {
		return err
	}
	_, _ = h.Pool.Exec(ctx, `
		UPDATE invoices SET status='paid', paid_at=NOW(), updated_at=NOW()
		 WHERE company_id=$1 AND id=$2`, cid, invID)
	var rID uuid.UUID
	_ = h.Pool.QueryRow(ctx,
		`SELECT reservation_id FROM invoices WHERE id=$1`, invID).Scan(&rID)
	if rID != uuid.Nil {
		_, _ = h.Pool.Exec(ctx,
			`UPDATE reservations SET paid_cents = paid_cents + $1 WHERE id=$2`, sess.AmountTotal, rID)
	}
	return nil
}

func (h *Handler) markSessionFailed(ctx context.Context, cid uuid.UUID, data json.RawMessage) error {
	var s stripeSession
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	_, _ = h.Pool.Exec(ctx, `
		UPDATE payments SET status='failed', updated_at=NOW()
		 WHERE company_id=$1 AND stripe_session_id=$2`, cid, s.Object.ID)
	return nil
}

func (h *Handler) markRefund(ctx context.Context, cid uuid.UUID, data json.RawMessage) error {
	var env struct {
		Object struct {
			PaymentIntent string `json:"payment_intent"`
			AmountRefunded int   `json:"amount_refunded"`
		} `json:"object"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	if env.Object.PaymentIntent == "" {
		return nil
	}
	_, _ = h.Pool.Exec(ctx, `
		UPDATE payments SET status='refunded', updated_at=NOW()
		 WHERE company_id=$1 AND stripe_payment_intent=$2`, cid, env.Object.PaymentIntent)
	return nil
}

// =============================================================================
// Signature verification
// =============================================================================

// verifyStripeSignature implements the standard "t=<ts>,v1=<sig>" header
// scheme. tolerance guards against replay attacks.
func verifyStripeSignature(header string, body []byte, secret string, tolerance time.Duration) bool {
	if header == "" || secret == "" {
		return false
	}
	var ts int64
	var v1 []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, _ = strconv.ParseInt(kv[1], 10, 64)
		case "v1":
			v1 = append(v1, kv[1])
		}
	}
	if ts == 0 || len(v1) == 0 {
		return false
	}
	// Tolerate clock skew / replay window.
	if abs(time.Now().Unix()-ts) > int64(tolerance.Seconds()) {
		return false
	}
	payload := strconv.FormatInt(ts, 10) + "." + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, cand := range v1 {
		if hmac.Equal([]byte(cand), []byte(expected)) {
			return true
		}
	}
	return false
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
