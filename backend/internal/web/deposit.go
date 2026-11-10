package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// =============================================================================
// Security deposit — per-reservation refundable hold
//
// At booking time BookPost seeds reservations.security_deposit_cents from the
// vehicle, falling back to the company default. Admin can edit the amount
// on the reservation page or trigger a Stripe Checkout Session to collect
// the deposit. The resulting payments row is marked reference='security_deposit'
// so reports / invoice math exclude it from rental revenue.
// =============================================================================

// ReservationDepositSet lets admin override the deposit amount for a booking.
// POST /admin/reservations/{id}/deposit
func (h *Handler) ReservationDepositSet(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	amt := parseDollarsOrCents(r.FormValue("amountDollars"), r.FormValue("amountCents"))
	if amt < 0 {
		amt = 0
	}
	_, err := h.Pool.Exec(r.Context(),
		`UPDATE reservations SET security_deposit_cents=$1, updated_at=NOW()
		 WHERE company_id=$2 AND id=$3`, amt, cid, id)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+id+"?err="+err.Error()+"#deposit", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Deposit+amount+updated#deposit", http.StatusSeeOther)
}

// ReservationDepositCharge creates a Stripe Checkout session for the deposit
// amount and records a 'pending' security_deposit payment row.
// POST /admin/reservations/{id}/deposit/charge
func (h *Handler) ReservationDepositCharge(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	ridStr := chi.URLParam(r, "id")
	rid, err := uuid.Parse(ridStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cfg := h.stripeCfg(r.Context(), cid)
	if !cfg.Configured {
		http.Redirect(w, r, "/admin/reservations/"+ridStr+"?err=Configure+Stripe+first#deposit", http.StatusSeeOther)
		return
	}
	// Pull booking + deposit + customer email.
	var booking, customerEmail string
	var deposit int
	var customerID uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `
		SELECT r.booking_number, r.security_deposit_cents, COALESCE(cu.id, '00000000-0000-0000-0000-000000000000'::uuid), COALESCE(cu.email,'')
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, rid).Scan(&booking, &deposit, &customerID, &customerEmail)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+ridStr+"?err=Reservation+not+found#deposit", http.StatusSeeOther)
		return
	}
	if deposit <= 0 {
		http.Redirect(w, r, "/admin/reservations/"+ridStr+"?err=Set+deposit+amount+first#deposit", http.StatusSeeOther)
		return
	}

	base := h.shareBase(r)
	args := url.Values{}
	args.Set("mode", "payment")
	args.Set("payment_method_types[]", "card")
	args.Set("line_items[0][quantity]", "1")
	args.Set("line_items[0][price_data][currency]", "usd")
	args.Set("line_items[0][price_data][product_data][name]",
		fmt.Sprintf("Security deposit (refundable) · Booking %s", booking))
	args.Set("line_items[0][price_data][unit_amount]", strconv.Itoa(deposit))
	args.Set("success_url", base+"/payments/success?inv=")
	args.Set("cancel_url", base+"/admin/reservations/"+ridStr)
	args.Set("client_reference_id", rid.String())
	args.Set("metadata[reservation_id]", rid.String())
	args.Set("metadata[company_id]", cid.String())
	args.Set("metadata[purpose]", "security_deposit")
	if customerEmail != "" {
		args.Set("customer_email", customerEmail)
	}
	sessID, sessURL, err := h.stripeCreateSession(cfg, args)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+ridStr+"?err=Stripe%3A+"+err.Error()+"#deposit", http.StatusSeeOther)
		return
	}
	// Record pending deposit row — reference marks it so reports exclude it from revenue.
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO payments (company_id, reservation_id, customer_id, amount_cents, method, status,
		                      stripe_session_id, stripe_mode, reference)
		VALUES ($1,$2::uuid,NULLIF($3::uuid,'00000000-0000-0000-0000-000000000000'),$4,'stripe','pending',$5,$6,'security_deposit')
		ON CONFLICT (stripe_session_id) DO NOTHING
	`, cid, rid, customerID, deposit, sessID, cfg.Mode)
	flash := url.QueryEscape("Deposit link ready: " + sessURL)
	http.Redirect(w, r, "/admin/reservations/"+ridStr+"?flash="+flash+"&paylink="+url.QueryEscape(sessURL)+"#deposit", http.StatusSeeOther)
}

// ReservationDepositRefund marks a charged deposit as refunded locally AND
// kicks off a Stripe refund against the original PaymentIntent, if available.
// POST /admin/reservations/{id}/deposit/refund
func (h *Handler) ReservationDepositRefund(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	cfg := h.stripeCfg(r.Context(), cid)

	// Find the most recent successful deposit payment on this reservation.
	var paymentID uuid.UUID
	var pi string
	var amount int
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, COALESCE(stripe_payment_intent,''), amount_cents FROM payments
		 WHERE company_id=$1 AND reservation_id=$2 AND reference='security_deposit' AND status='succeeded'
		 ORDER BY created_at DESC LIMIT 1
	`, cid, id).Scan(&paymentID, &pi, &amount)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+id+"?err=No+deposit+to+refund#deposit", http.StatusSeeOther)
		return
	}

	// Attempt the Stripe refund if we have a PaymentIntent + valid key.
	if pi != "" && cfg.SecretKey != "" {
		rq, _ := http.NewRequest("POST", "https://api.stripe.com/v1/refunds",
			strings.NewReader("payment_intent="+pi))
		rq.Header.Set("Authorization", "Bearer "+cfg.SecretKey)
		rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, rerr := http.DefaultClient.Do(rq)
		if rerr != nil {
			http.Redirect(w, r, "/admin/reservations/"+id+"?err=Stripe+refund%3A+"+rerr.Error()+"#deposit", http.StatusSeeOther)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			http.Redirect(w, r, "/admin/reservations/"+id+"?err=Stripe+refund+failed#deposit", http.StatusSeeOther)
			return
		}
	}

	// Mark locally — the webhook (refund.updated / charge.refunded) will
	// also flip status if the Stripe call succeeded.
	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE payments SET status='refunded', updated_at=NOW() WHERE id=$1`, paymentID)
	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE reservations SET deposit_refunded_at=NOW() WHERE company_id=$1 AND id=$2`, cid, id)
	_ = s
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Deposit+refund+initiated#deposit", http.StatusSeeOther)
}
