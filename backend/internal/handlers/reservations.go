package handlers

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type reservationRow struct {
	ID             uuid.UUID  `json:"id"`
	BookingNumber  string     `json:"bookingNumber"`
	CustomerID     *uuid.UUID `json:"customerId"`
	CustomerName   *string    `json:"customerName"`
	VehicleID      *uuid.UUID `json:"vehicleId"`
	VehicleLabel   *string    `json:"vehicleLabel"`
	PickupBranchID *uuid.UUID `json:"pickupBranchId"`
	ReturnBranchID *uuid.UUID `json:"returnBranchId"`
	PickupAt       time.Time  `json:"pickupAt"`
	ReturnAt       time.Time  `json:"returnAt"`
	Status         string     `json:"status"`
	DailyRateCents int        `json:"dailyRateCents"`
	RentalDays     int        `json:"rentalDays"`
	SubtotalCents  int        `json:"subtotalCents"`
	ExtrasCents    int        `json:"extrasCents"`
	TaxCents       int        `json:"taxCents"`
	DepositCents   int        `json:"depositCents"`
	TotalCents     int        `json:"totalCents"`
	PaidCents      int        `json:"paidCents"`
	Source         string     `json:"source"`
	Notes          *string    `json:"notes"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func rentalDays(a, b time.Time) int {
	d := int(math.Ceil(b.Sub(a).Hours() / 24.0))
	if d < 1 {
		d = 1
	}
	return d
}

func (d *Deps) ListReservations(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	status := r.URL.Query().Get("status")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.customer_id,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE (cu.first_name || ' ' || cu.last_name) END,
		       r.vehicle_id,
		       CASE WHEN v.id IS NULL THEN NULL ELSE (COALESCE(v.year::text,'') || ' ' || COALESCE(v.make,'') || ' ' || COALESCE(v.model,'')) END,
		       r.pickup_branch_id, r.return_branch_id,
		       r.pickup_at, r.return_at, r.status, r.daily_rate_cents, r.rental_days,
		       r.subtotal_cents, r.extras_cents, r.tax_cents, r.deposit_cents, r.total_cents, r.paid_cents,
		       r.source, r.notes, r.created_at
		FROM reservations r
		LEFT JOIN customers cu ON cu.id = r.customer_id
		LEFT JOIN vehicles  v  ON v.id  = r.vehicle_id
		WHERE r.company_id=$1 AND ($2='' OR r.status=$2)
		ORDER BY r.pickup_at DESC LIMIT 500
	`, c.CompanyID, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	defer rows.Close()
	out := []reservationRow{}
	for rows.Next() {
		var rr reservationRow
		if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.CustomerID, &rr.CustomerName,
			&rr.VehicleID, &rr.VehicleLabel, &rr.PickupBranchID, &rr.ReturnBranchID,
			&rr.PickupAt, &rr.ReturnAt, &rr.Status, &rr.DailyRateCents, &rr.RentalDays,
			&rr.SubtotalCents, &rr.ExtrasCents, &rr.TaxCents, &rr.DepositCents, &rr.TotalCents, &rr.PaidCents,
			&rr.Source, &rr.Notes, &rr.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error: "+err.Error())
			return
		}
		out = append(out, rr)
	}
	writeJSON(w, http.StatusOK, out)
}

type createReservationReq struct {
	CustomerID     string    `json:"customerId"`
	VehicleID      string    `json:"vehicleId"`
	PickupBranchID string    `json:"pickupBranchId"`
	ReturnBranchID string    `json:"returnBranchId"`
	PickupAt       time.Time `json:"pickupAt"`
	ReturnAt       time.Time `json:"returnAt"`
	ExtrasCents    int       `json:"extrasCents"`
	TaxCents       int       `json:"taxCents"`
	DepositCents   int       `json:"depositCents"`
	Notes          string    `json:"notes"`
}

func (d *Deps) CreateReservation(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createReservationReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.PickupAt.IsZero() || req.ReturnAt.IsZero() || !req.ReturnAt.After(req.PickupAt) {
		writeErr(w, http.StatusBadRequest, "valid pickupAt/returnAt required (return after pickup)")
		return
	}
	if req.VehicleID == "" {
		writeErr(w, http.StatusBadRequest, "vehicleId required")
		return
	}
	var daily int
	err := d.DB.Pool.QueryRow(r.Context(),
		`SELECT daily_rate_cents FROM vehicles WHERE company_id=$1 AND id=$2`,
		c.CompanyID, req.VehicleID).Scan(&daily)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown vehicle for this company")
		return
	}
	days := rentalDays(req.PickupAt, req.ReturnAt)
	subtotal := daily * days
	total := subtotal + req.ExtrasCents + req.TaxCents

	var id, bookingNumber string
	err = d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO reservations (company_id, customer_id, vehicle_id, pickup_branch_id, return_branch_id,
		  pickup_at, return_at, status, daily_rate_cents, rental_days, subtotal_cents, extras_cents, tax_cents,
		  deposit_cents, total_cents, source, notes)
		VALUES ($1, NULLIF($2,'')::uuid, $3::uuid, NULLIF($4,'')::uuid, NULLIF($5,'')::uuid,
		        $6, $7, 'pending', $8, $9, $10, $11, $12, $13, $14, 'manager', NULLIF($15,''))
		RETURNING id, booking_number
	`, c.CompanyID, req.CustomerID, req.VehicleID, req.PickupBranchID, req.ReturnBranchID,
		req.PickupAt, req.ReturnAt, daily, days, subtotal, req.ExtrasCents, req.TaxCents,
		req.DepositCents, total, req.Notes).Scan(&id, &bookingNumber)
	if err != nil {
		if strings.Contains(err.Error(), "reservations_no_overlap") {
			writeErr(w, http.StatusConflict, "vehicle not available for those dates")
			return
		}
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "bookingNumber": bookingNumber, "totalCents": total})
}

type patchReservationReq struct {
	Status *string `json:"status"`
	Notes  *string `json:"notes"`
}

var allowedStatus = map[string]bool{
	"inquiry": true, "pending": true, "confirmed": true,
	"checked_out": true, "returned": true, "cancelled": true, "no_show": true,
}

func (d *Deps) UpdateReservation(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var req patchReservationReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Status != nil {
		if !allowedStatus[*req.Status] {
			writeErr(w, http.StatusBadRequest, "invalid status")
			return
		}
		if _, err := d.DB.Pool.Exec(r.Context(),
			`UPDATE reservations SET status=$1 WHERE company_id=$2 AND id=$3`,
			*req.Status, c.CompanyID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, "update failed")
			return
		}
		// Side-effect on the vehicle status.
		switch *req.Status {
		case "checked_out":
			_, _ = d.DB.Pool.Exec(r.Context(), `
				UPDATE vehicles SET status='rented'
				WHERE company_id=$1 AND id=(SELECT vehicle_id FROM reservations WHERE id=$2)
			`, c.CompanyID, id)
		case "returned", "cancelled", "no_show":
			_, _ = d.DB.Pool.Exec(r.Context(), `
				UPDATE vehicles SET status='available'
				WHERE company_id=$1 AND id=(SELECT vehicle_id FROM reservations WHERE id=$2)
			`, c.CompanyID, id)
		case "confirmed":
			_, _ = d.DB.Pool.Exec(r.Context(), `
				UPDATE vehicles SET status='reserved'
				WHERE company_id=$1 AND id=(SELECT vehicle_id FROM reservations WHERE id=$2)
				  AND status='available'
			`, c.CompanyID, id)
		}
	}
	if req.Notes != nil {
		if _, err := d.DB.Pool.Exec(r.Context(),
			`UPDATE reservations SET notes=$1 WHERE company_id=$2 AND id=$3`,
			*req.Notes, c.CompanyID, id); err != nil {
			writeErr(w, http.StatusInternalServerError, "update failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetReservation returns one booking with its payments and contract summary.
func (d *Deps) GetReservation(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var rr reservationRow
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT r.id, r.booking_number, r.customer_id,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE (cu.first_name || ' ' || cu.last_name) END,
		       r.vehicle_id,
		       CASE WHEN v.id IS NULL THEN NULL ELSE (COALESCE(v.year::text,'') || ' ' || COALESCE(v.make,'') || ' ' || COALESCE(v.model,'')) END,
		       r.pickup_branch_id, r.return_branch_id,
		       r.pickup_at, r.return_at, r.status, r.daily_rate_cents, r.rental_days,
		       r.subtotal_cents, r.extras_cents, r.tax_cents, r.deposit_cents, r.total_cents, r.paid_cents,
		       r.source, r.notes, r.created_at
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles  v  ON v.id=r.vehicle_id
		WHERE r.company_id=$1 AND r.id=$2
	`, c.CompanyID, id).Scan(&rr.ID, &rr.BookingNumber, &rr.CustomerID, &rr.CustomerName,
		&rr.VehicleID, &rr.VehicleLabel, &rr.PickupBranchID, &rr.ReturnBranchID,
		&rr.PickupAt, &rr.ReturnAt, &rr.Status, &rr.DailyRateCents, &rr.RentalDays,
		&rr.SubtotalCents, &rr.ExtrasCents, &rr.TaxCents, &rr.DepositCents, &rr.TotalCents, &rr.PaidCents,
		&rr.Source, &rr.Notes, &rr.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	payRows, _ := d.DB.Pool.Query(r.Context(), `
		SELECT id, amount_cents, currency, method, status, reference, note, created_at
		FROM payments WHERE company_id=$1 AND reservation_id=$2 ORDER BY created_at DESC
	`, c.CompanyID, id)
	type pay struct {
		ID        uuid.UUID `json:"id"`
		Amount    int       `json:"amountCents"`
		Currency  string    `json:"currency"`
		Method    string    `json:"method"`
		Status    string    `json:"status"`
		Reference *string   `json:"reference"`
		Note      *string   `json:"note"`
		CreatedAt time.Time `json:"createdAt"`
	}
	pays := []pay{}
	if payRows != nil {
		defer payRows.Close()
		for payRows.Next() {
			var p pay
			if err := payRows.Scan(&p.ID, &p.Amount, &p.Currency, &p.Method, &p.Status, &p.Reference, &p.Note, &p.CreatedAt); err == nil {
				pays = append(pays, p)
			}
		}
	}

	type contract struct {
		ID        uuid.UUID  `json:"id"`
		Version   int        `json:"version"`
		Status    string     `json:"status"`
		SignedAt  *time.Time `json:"signedAt"`
		SignerName *string   `json:"signerName"`
		SignToken *string    `json:"signToken"`
	}
	var cs []contract
	cRows, _ := d.DB.Pool.Query(r.Context(), `
		SELECT id, version, status, signed_at, signer_name, sign_token
		FROM rental_contracts WHERE company_id=$1 AND reservation_id=$2 ORDER BY version DESC
	`, c.CompanyID, id)
	if cRows != nil {
		defer cRows.Close()
		for cRows.Next() {
			var ct contract
			if err := cRows.Scan(&ct.ID, &ct.Version, &ct.Status, &ct.SignedAt, &ct.SignerName, &ct.SignToken); err == nil {
				cs = append(cs, ct)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"reservation": rr,
		"payments":    pays,
		"contracts":   cs,
	})
}

// Availability check: given a vehicle and a date range, is it bookable?
func (d *Deps) CheckAvailability(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	vehicleID := r.URL.Query().Get("vehicleId")
	from, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if vehicleID == "" || err1 != nil || err2 != nil || !to.After(from) {
		writeErr(w, http.StatusBadRequest, "vehicleId, from, to (RFC3339) required")
		return
	}
	var hit int
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM reservations
		WHERE company_id=$1 AND vehicle_id=$2
		  AND status IN ('pending','confirmed','checked_out')
		  AND tstzrange(pickup_at, return_at, '[)') && tstzrange($3,$4,'[)')
	`, c.CompanyID, vehicleID, from, to).Scan(&hit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	var blocked int
	_ = d.DB.Pool.QueryRow(r.Context(), `
		SELECT COUNT(*) FROM blocked_periods
		WHERE vehicle_id=$1 AND tstzrange(starts_at, ends_at, '[)') && tstzrange($2,$3,'[)')
	`, vehicleID, from, to).Scan(&blocked)
	writeJSON(w, http.StatusOK, map[string]any{
		"available": hit == 0 && blocked == 0,
		"overlapReservations": hit,
		"overlapBlocks": blocked,
	})
}

// Payments: record a payment against a reservation.
type createPaymentReq struct {
	ReservationID string `json:"reservationId"`
	AmountCents   int    `json:"amountCents"`
	Method        string `json:"method"`
	Reference     string `json:"reference"`
	Note          string `json:"note"`
}

func (d *Deps) CreatePayment(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createPaymentReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.AmountCents <= 0 || req.ReservationID == "" {
		writeErr(w, http.StatusBadRequest, "amountCents > 0 and reservationId required")
		return
	}
	if req.Method == "" {
		req.Method = "manual"
	}
	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	var id string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO payments (company_id, reservation_id, amount_cents, method, status, reference, note)
		VALUES ($1,$2::uuid,$3,$4,'succeeded',NULLIF($5,''),NULLIF($6,''))
		RETURNING id
	`, c.CompanyID, req.ReservationID, req.AmountCents, req.Method, req.Reference, req.Note).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	_, err = tx.Exec(r.Context(), `
		UPDATE reservations SET paid_cents = paid_cents + $1
		WHERE company_id=$2 AND id=$3
	`, req.AmountCents, c.CompanyID, req.ReservationID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// Contracts: generate (draft) + sign (via token).
type createContractReq struct {
	ReservationID string `json:"reservationId"`
	Body          string `json:"body"`
	SignerName    string `json:"signerName"`
	SignerEmail   string `json:"signerEmail"`
}

func (d *Deps) CreateContract(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createContractReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ReservationID == "" {
		writeErr(w, http.StatusBadRequest, "reservationId required")
		return
	}
	if req.Body == "" {
		req.Body = "This rental agreement is entered into between the rental company and the customer. " +
			"Vehicle is rented for the dates stated on the booking. Customer agrees to pay all charges, " +
			"return the vehicle in equivalent condition, and is liable for traffic violations."
	}
	tok := uuid.NewString()
	exp := time.Now().Add(14 * 24 * time.Hour)
	var id string
	var ver int
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO rental_contracts (company_id, reservation_id, version, body, status, signer_name, signer_email, sign_token, sign_token_exp)
		VALUES ($1,$2::uuid,
		        COALESCE((SELECT MAX(version)+1 FROM rental_contracts WHERE reservation_id=$2::uuid),1),
		        $3,'sent',NULLIF($4,''),NULLIF($5,''),$6,$7)
		RETURNING id, version
	`, c.CompanyID, req.ReservationID, req.Body, req.SignerName, req.SignerEmail, tok, exp).Scan(&id, &ver)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "version": ver, "signToken": tok, "signUrl": "/sign/" + tok})
}

type signReq struct {
	SignerName string `json:"signerName"`
	Signature  string `json:"signature"` // base64 image or typed name
}

// PublicSignContract is unauthenticated: anyone with the token can sign.
func (d *Deps) PublicSignContract(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var req signReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.SignerName) == "" || strings.TrimSpace(req.Signature) == "" {
		writeErr(w, http.StatusBadRequest, "signerName and signature required")
		return
	}
	ct, err := d.DB.Pool.Exec(r.Context(), `
		UPDATE rental_contracts
		   SET status='signed', signed_at=NOW(), signer_name=$1, signature_data=$2
		 WHERE sign_token=$3 AND status IN ('draft','sent') AND sign_token_exp > NOW()
	`, req.SignerName, req.Signature, tok)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if ct.RowsAffected() == 0 {
		writeErr(w, http.StatusGone, "sign link invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PublicGetContract: fetch body for the signer page.
func (d *Deps) PublicGetContract(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var body string
	var status string
	var signer *string
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT body, status, signer_name FROM rental_contracts
		WHERE sign_token=$1 AND sign_token_exp > NOW()
	`, tok).Scan(&body, &status, &signer)
	if err != nil {
		writeErr(w, http.StatusNotFound, "sign link invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"body": body, "status": status, "signerName": signer})
}
