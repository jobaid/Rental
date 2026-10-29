package handlers

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rentalsoftware/backend/internal/auth"
)

// Public company directory: lists active companies so the booking site can route.
func (d *Deps) PublicCompanies(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.Pool.Query(r.Context(),
		`SELECT slug, name FROM companies WHERE status='active' ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type c struct{ Slug, Name string }
	out := []c{}
	for rows.Next() {
		var row c
		_ = rows.Scan(&row.Slug, &row.Name)
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// PublicSearch: find available vehicles across (or within one) tenant for a date range.
func (d *Deps) PublicSearch(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("company"))
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	from, err1 := time.Parse(time.RFC3339, fromStr)
	to, err2 := time.Parse(time.RFC3339, toStr)
	if err1 != nil || err2 != nil || !to.After(from) {
		writeErr(w, http.StatusBadRequest, "from, to (RFC3339) required and to>from")
		return
	}
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT v.id, c.slug, c.name, v.make, v.model, v.year, v.seats, v.transmission, v.fuel_type,
		       v.daily_rate_cents, v.deposit_cents
		FROM vehicles v JOIN companies c ON c.id=v.company_id
		WHERE c.status='active' AND v.status IN ('available','reserved')
		  AND ($1='' OR c.slug=$1)
		  AND NOT EXISTS (
		    SELECT 1 FROM reservations r
		    WHERE r.vehicle_id=v.id AND r.status IN ('pending','confirmed','checked_out')
		      AND tstzrange(r.pickup_at, r.return_at, '[)') && tstzrange($2,$3,'[)')
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM blocked_periods b
		    WHERE b.vehicle_id=v.id
		      AND tstzrange(b.starts_at, b.ends_at, '[)') && tstzrange($2,$3,'[)')
		  )
		ORDER BY v.daily_rate_cents ASC LIMIT 100
	`, slug, from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	defer rows.Close()
	type v struct {
		ID             uuid.UUID `json:"id"`
		CompanySlug    string    `json:"companySlug"`
		CompanyName    string    `json:"companyName"`
		Make           *string   `json:"make"`
		Model          *string   `json:"model"`
		Year           *int      `json:"year"`
		Seats          *int      `json:"seats"`
		Transmission   *string   `json:"transmission"`
		FuelType       *string   `json:"fuelType"`
		DailyRateCents int       `json:"dailyRateCents"`
		DepositCents   int       `json:"depositCents"`
	}
	out := []v{}
	for rows.Next() {
		var row v
		if err := rows.Scan(&row.ID, &row.CompanySlug, &row.CompanyName, &row.Make, &row.Model, &row.Year, &row.Seats,
			&row.Transmission, &row.FuelType, &row.DailyRateCents, &row.DepositCents); err == nil {
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type publicBookReq struct {
	CompanySlug string    `json:"companySlug"`
	VehicleID   string    `json:"vehicleId"`
	PickupAt    time.Time `json:"pickupAt"`
	ReturnAt    time.Time `json:"returnAt"`
	FirstName   string    `json:"firstName"`
	LastName    string    `json:"lastName"`
	Email       string    `json:"email"`
	Phone       string    `json:"phone"`
	Password    string    `json:"password"` // optional, creates/updates portal account
}

type publicBookResp struct {
	BookingNumber string `json:"bookingNumber"`
	TotalCents    int    `json:"totalCents"`
	Token         string `json:"token,omitempty"`
}

// PublicBook: unauthenticated. Creates a customer + reservation. Overlap/block protected by exclusion constraints.
func (d *Deps) PublicBook(w http.ResponseWriter, r *http.Request) {
	var req publicBookReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.VehicleID == "" || req.PickupAt.IsZero() || req.ReturnAt.IsZero() ||
		!req.ReturnAt.After(req.PickupAt) || req.FirstName == "" || req.LastName == "" || req.Email == "" {
		writeErr(w, http.StatusBadRequest, "all booking fields required")
		return
	}

	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	// Resolve company from the vehicle (and verify slug if provided).
	var companyID uuid.UUID
	var dailyRate int
	err = tx.QueryRow(r.Context(), `
		SELECT v.company_id, v.daily_rate_cents
		FROM vehicles v JOIN companies c ON c.id=v.company_id
		WHERE v.id=$1 AND c.status='active' AND ($2='' OR c.slug=$2)
	`, req.VehicleID, req.CompanySlug).Scan(&companyID, &dailyRate)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "vehicle not available")
		return
	}

	// Find-or-create a customer (per-tenant, matched on email).
	var customerID string
	_ = tx.QueryRow(r.Context(),
		`SELECT id FROM customers WHERE company_id=$1 AND LOWER(email)=$2 LIMIT 1`,
		companyID, req.Email).Scan(&customerID)
	if customerID == "" {
		err = tx.QueryRow(r.Context(), `
			INSERT INTO customers (company_id, first_name, last_name, email, phone)
			VALUES ($1,$2,$3,$4,NULLIF($5,'')) RETURNING id
		`, companyID, req.FirstName, req.LastName, req.Email, req.Phone).Scan(&customerID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "customer create failed: "+err.Error())
			return
		}
	}

	// Optionally create a customer-role portal user.
	var token string
	if req.Password != "" && len(req.Password) >= 8 {
		hash, err := auth.HashPassword(req.Password)
		if err == nil {
			var userID uuid.UUID
			err = tx.QueryRow(r.Context(), `
				INSERT INTO users (company_id, email, password_hash, name, role, is_active, email_verified_at)
				VALUES ($1,$2,$3,$4,'customer',TRUE, NOW())
				ON CONFLICT (company_id, email) DO UPDATE SET password_hash=EXCLUDED.password_hash
				RETURNING id
			`, companyID, req.Email, hash, req.FirstName+" "+req.LastName).Scan(&userID)
			if err == nil {
				_, _ = tx.Exec(r.Context(), `UPDATE customers SET user_id=$1 WHERE id=$2`, userID, customerID)
				cid := companyID
				tok, _ := d.Signer.Sign(userID, &cid, "customer")
				token = tok
			}
		}
	}

	days := rentalDays(req.PickupAt, req.ReturnAt)
	subtotal := dailyRate * days
	total := subtotal

	var bookingNumber string
	err = tx.QueryRow(r.Context(), `
		INSERT INTO reservations (company_id, customer_id, vehicle_id, pickup_at, return_at, status,
		  daily_rate_cents, rental_days, subtotal_cents, total_cents, source)
		VALUES ($1,$2::uuid,$3::uuid,$4,$5,'pending',$6,$7,$8,$9,'customer')
		RETURNING booking_number
	`, companyID, customerID, req.VehicleID, req.PickupAt, req.ReturnAt, dailyRate, days, subtotal, total).Scan(&bookingNumber)
	if err != nil {
		if strings.Contains(err.Error(), "reservations_no_overlap") {
			writeErr(w, http.StatusConflict, "vehicle just got booked for those dates")
			return
		}
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}

	// Notification stub.
	_, _ = tx.Exec(r.Context(), `
		INSERT INTO notifications (company_id, kind, recipient, subject, body, status)
		VALUES ($1,'booking_confirmation',$2,'Your booking is confirmed',$3,'queued')
	`, companyID, req.Email, "Booking "+bookingNumber+" received. Total: $"+centsToStr(total))

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit error")
		return
	}
	writeJSON(w, http.StatusCreated, publicBookResp{BookingNumber: bookingNumber, TotalCents: total, Token: token})
}

func centsToStr(c int) string {
	d := c / 100
	cents := c % 100
	return itoa(d) + "." + pad2(cents)
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	b := [20]byte{}
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	s := string(b[i:])
	if neg {
		s = "-" + s
	}
	return s
}
func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// Public: fetch a booking by number (unauthenticated by number — use with care; here it's demo-grade).
func (d *Deps) PublicLookupBooking(w http.ResponseWriter, r *http.Request) {
	num := chi.URLParam(r, "number")
	email := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
	if num == "" || email == "" {
		writeErr(w, http.StatusBadRequest, "booking number and email required")
		return
	}
	var out struct {
		BookingNumber string    `json:"bookingNumber"`
		Status        string    `json:"status"`
		PickupAt      time.Time `json:"pickupAt"`
		ReturnAt      time.Time `json:"returnAt"`
		TotalCents    int       `json:"totalCents"`
		PaidCents     int       `json:"paidCents"`
		Vehicle       string    `json:"vehicle"`
	}
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT r.booking_number, r.status, r.pickup_at, r.return_at, r.total_cents, r.paid_cents,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'')
		FROM reservations r
		JOIN customers c ON c.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.booking_number=$1 AND LOWER(c.email)=$2
	`, num, email).Scan(&out.BookingNumber, &out.Status, &out.PickupAt, &out.ReturnAt, &out.TotalCents, &out.PaidCents, &out.Vehicle)
	if err != nil {
		writeErr(w, http.StatusNotFound, "booking not found for that email")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Portal: a logged-in 'customer' user lists their reservations.
func (d *Deps) PortalMyBookings(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at, r.total_cents, r.paid_cents,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'')
		FROM reservations r
		JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE cu.user_id=$1 AND r.company_id=$2
		ORDER BY r.pickup_at DESC
	`, c.UserID, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID            uuid.UUID `json:"id"`
		BookingNumber string    `json:"bookingNumber"`
		Status        string    `json:"status"`
		PickupAt      time.Time `json:"pickupAt"`
		ReturnAt      time.Time `json:"returnAt"`
		TotalCents    int       `json:"totalCents"`
		PaidCents     int       `json:"paidCents"`
		Vehicle       string    `json:"vehicle"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.BookingNumber, &r.Status, &r.PickupAt, &r.ReturnAt, &r.TotalCents, &r.PaidCents, &r.Vehicle); err == nil {
			out = append(out, r)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
