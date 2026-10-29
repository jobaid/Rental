package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// --- Catalogs (public, read-only) ---

func (d *Deps) PublicAddons(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT a.code, a.name, a.description, a.price_cents, a.price_per, a.display_order
		FROM addon_catalog a JOIN companies c ON c.id = a.company_id
		WHERE c.slug=$1 AND a.is_active=TRUE
		ORDER BY a.display_order, a.name
	`, slug)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type a struct {
		Code  string  `json:"code"`
		Name  string  `json:"name"`
		Desc  *string `json:"description"`
		Price int     `json:"priceCents"`
		Per   string  `json:"pricePer"`
	}
	out := []a{}
	for rows.Next() {
		var row a
		var order int
		if err := rows.Scan(&row.Code, &row.Name, &row.Desc, &row.Price, &row.Per, &order); err == nil {
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) PublicInsurance(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT i.code, i.name, i.description, i.price_per_day_cents, i.deductible_cents, i.is_default
		FROM insurance_catalog i JOIN companies c ON c.id = i.company_id
		WHERE c.slug=$1 AND i.is_active=TRUE
		ORDER BY i.price_per_day_cents
	`, slug)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type i struct {
		Code       string  `json:"code"`
		Name       string  `json:"name"`
		Desc       *string `json:"description"`
		PerDay     int     `json:"pricePerDayCents"`
		Deductible int     `json:"deductibleCents"`
		Default    bool    `json:"isDefault"`
	}
	out := []i{}
	for rows.Next() {
		var row i
		if err := rows.Scan(&row.Code, &row.Name, &row.Desc, &row.PerDay, &row.Deductible, &row.Default); err == nil {
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- License verification stub ---
// This is an automated plausibility check, NOT a real DMV lookup. It:
//   - Validates length (6–20 chars) and chars (alnum + dashes)
//   - Checks a checksum rule for a common US-style pattern (letter+digits)
//   - Flags obvious fakes (all-same, "TEST", repeating patterns)
// Real verification requires a DMV/third-party API (AAMVA, VerifyAnyLicense).
// Result is persisted + manual_status is left 'pending' for the shop manager to approve.
func licenseChecksumValid(license string) (bool, string) {
	l := strings.ToUpper(strings.TrimSpace(license))
	if len(l) < 6 || len(l) > 20 {
		return false, "length out of range (6–20)"
	}
	for _, r := range l {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
			return false, "contains unsupported characters"
		}
	}
	// Obvious fakes.
	if strings.Contains(l, "TEST") || strings.Contains(l, "FAKE") || strings.Contains(l, "SAMPLE") {
		return false, "contains obvious placeholder text"
	}
	same := true
	for i := 1; i < len(l); i++ {
		if l[i] != l[0] {
			same = false
			break
		}
	}
	if same {
		return false, "all characters identical"
	}
	// Entropy heuristic: at least 3 distinct chars.
	seen := map[byte]struct{}{}
	for i := 0; i < len(l); i++ {
		seen[l[i]] = struct{}{}
	}
	if len(seen) < 3 {
		return false, "too few distinct characters"
	}
	return true, "plausibility check passed; manual review required"
}

type verifyReq struct {
	ReservationID string `json:"reservationId"`
	CustomerID    string `json:"customerId"`
	LicenseNumber string `json:"licenseNumber"`
	Country       string `json:"issuingCountry"`
	State         string `json:"issuingState"`
	ExpiresOn     string `json:"expiresOn"`
}

// Public (unauthenticated) license submission from the booking flow.
func (d *Deps) PublicVerifyLicense(w http.ResponseWriter, r *http.Request) {
	var req verifyReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.LicenseNumber == "" {
		writeErr(w, http.StatusBadRequest, "licenseNumber required")
		return
	}
	ok, note := licenseChecksumValid(req.LicenseNumber)
	// Expiry check if provided.
	if req.ExpiresOn != "" {
		t, err := time.Parse("2006-01-02", req.ExpiresOn)
		if err == nil && t.Before(time.Now()) {
			ok = false
			note = "license expired"
		}
	}

	// Associate with a company if we have a reservation.
	var cid *uuid.UUID
	if req.ReservationID != "" {
		var tmp uuid.UUID
		if err := d.DB.Pool.QueryRow(r.Context(),
			`SELECT company_id FROM reservations WHERE id=$1`, req.ReservationID).Scan(&tmp); err == nil {
			cid = &tmp
		}
	}
	if cid != nil {
		_, _ = d.DB.Pool.Exec(r.Context(), `
			INSERT INTO license_verifications
			  (company_id, customer_id, reservation_id, license_number, issuing_country, issuing_state,
			   expires_on, checksum_valid, automated_note, manual_status)
			VALUES ($1, NULLIF($2,'')::uuid, $3::uuid, $4, NULLIF($5,''), NULLIF($6,''),
			        NULLIF($7,'')::date, $8, $9, 'pending')
		`, cid, req.CustomerID, req.ReservationID, strings.ToUpper(req.LicenseNumber),
			req.Country, req.State, req.ExpiresOn, ok, note)
		// Keep the customer record in sync.
		if req.CustomerID != "" {
			_, _ = d.DB.Pool.Exec(r.Context(), `
				UPDATE customers SET license_number=$1, license_expiry=NULLIF($2,'')::date
				WHERE id=$3 AND company_id=$4
			`, strings.ToUpper(req.LicenseNumber), req.ExpiresOn, req.CustomerID, cid)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"checksumValid":  ok,
		"automatedNote":  note,
		"manualStatus":   "pending",
		"advisory":       "Automated plausibility check only. A manager must verify the physical license at pickup.",
	})
}

// Manager: list pending license verifications.
func (d *Deps) ListLicenseVerifications(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT lv.id, lv.reservation_id, lv.customer_id, lv.license_number, lv.issuing_country, lv.issuing_state,
		       lv.expires_on::text, lv.checksum_valid, lv.automated_note, lv.manual_status, lv.created_at,
		       COALESCE(cu.first_name||' '||cu.last_name,''), r.booking_number
		FROM license_verifications lv
		LEFT JOIN customers cu ON cu.id=lv.customer_id
		LEFT JOIN reservations r ON r.id=lv.reservation_id
		WHERE lv.company_id=$1 ORDER BY lv.created_at DESC LIMIT 200
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID            uuid.UUID  `json:"id"`
		ReservationID *uuid.UUID `json:"reservationId"`
		CustomerID    *uuid.UUID `json:"customerId"`
		License       string     `json:"licenseNumber"`
		Country       *string    `json:"country"`
		State         *string    `json:"state"`
		ExpiresOn     *string    `json:"expiresOn"`
		ChecksumValid *bool      `json:"checksumValid"`
		Note          *string    `json:"automatedNote"`
		Status        string     `json:"manualStatus"`
		Created       time.Time  `json:"createdAt"`
		Customer      string     `json:"customer"`
		Booking       *string    `json:"bookingNumber"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.ReservationID, &r.CustomerID, &r.License, &r.Country, &r.State,
			&r.ExpiresOn, &r.ChecksumValid, &r.Note, &r.Status, &r.Created, &r.Customer, &r.Booking); err == nil {
			out = append(out, r)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) ReviewLicense(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var body struct {
		Status string `json:"status"` // approved | rejected
	}
	if err := decode(r, &body); err != nil || (body.Status != "approved" && body.Status != "rejected") {
		writeErr(w, http.StatusBadRequest, "status must be approved|rejected")
		return
	}
	_, err := d.DB.Pool.Exec(r.Context(), `
		UPDATE license_verifications
		   SET manual_status=$1, reviewed_by=$2, reviewed_at=NOW()
		 WHERE company_id=$3 AND id=$4
	`, body.Status, c.UserID, c.CompanyID, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Photo upload tokens ---

func randToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type sendLinkReq struct {
	ReservationID string `json:"reservationId"`
	Kind          string `json:"kind"` // before | after | both
}

// Manager creates a one-time upload link + logs an outbound email.
func (d *Deps) CreatePhotoUploadLink(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req sendLinkReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ReservationID == "" {
		writeErr(w, http.StatusBadRequest, "reservationId required")
		return
	}
	if req.Kind == "" {
		req.Kind = "both"
	}

	tok := randToken()
	exp := time.Now().Add(14 * 24 * time.Hour)
	_, err := d.DB.Pool.Exec(r.Context(), `
		INSERT INTO photo_upload_tokens (token, company_id, reservation_id, kind, expires_at, created_by)
		VALUES ($1,$2,$3::uuid,$4,$5,$6)
	`, tok, c.CompanyID, req.ReservationID, req.Kind, exp, c.UserID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}

	// Resolve recipient and queue a notification.
	var email string
	_ = d.DB.Pool.QueryRow(r.Context(), `
		SELECT cu.email FROM reservations r JOIN customers cu ON cu.id=r.customer_id
		WHERE r.id=$1
	`, req.ReservationID).Scan(&email)
	publicUrl := d.PublicBase + "/upload/" + tok
	_, _ = d.DB.Pool.Exec(r.Context(), `
		INSERT INTO notifications (company_id, kind, recipient, subject, body, status)
		VALUES ($1,'photo_upload_link',$2,$3,$4,'queued')
	`, c.CompanyID, email,
		"Upload your pickup/return photos",
		"Please upload your "+req.Kind+" photos: "+publicUrl)

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":   tok,
		"url":     publicUrl,
		"expires": exp,
	})
}

type publicUploadInfo struct {
	Kind         string `json:"kind"`
	Expired      bool   `json:"expired"`
	Used         bool   `json:"used"`
	Booking      string `json:"bookingNumber"`
	Vehicle      string `json:"vehicle"`
	Customer     string `json:"customer"`
}

// Public: show the upload page info (no auth).
func (d *Deps) PublicUploadInfo(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var info publicUploadInfo
	var exp time.Time
	var used *time.Time
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT pt.kind, pt.expires_at, pt.used_at,
		       r.booking_number,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       COALESCE(cu.first_name||' '||cu.last_name,'')
		FROM photo_upload_tokens pt
		JOIN reservations r ON r.id=pt.reservation_id
		LEFT JOIN vehicles  v  ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE pt.token=$1
	`, tok).Scan(&info.Kind, &exp, &used, &info.Booking, &info.Vehicle, &info.Customer)
	if err != nil {
		writeErr(w, http.StatusNotFound, "link invalid")
		return
	}
	info.Expired = time.Now().After(exp)
	info.Used = used != nil
	writeJSON(w, http.StatusOK, info)
}

// Public: upload a photo via the token (ties to the booking's vehicle; stored under uploads/public/<token>).
func (d *Deps) PublicUploadPhoto(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var companyID, resID, vehID uuid.UUID
	var kind string
	var exp time.Time
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT pt.company_id, pt.reservation_id, r.vehicle_id, pt.kind, pt.expires_at
		FROM photo_upload_tokens pt JOIN reservations r ON r.id=pt.reservation_id
		WHERE pt.token=$1
	`, tok).Scan(&companyID, &resID, &vehID, &kind, &exp)
	if err != nil {
		writeErr(w, http.StatusNotFound, "link invalid")
		return
	}
	if time.Now().After(exp) {
		writeErr(w, http.StatusGone, "link expired")
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart parse")
		return
	}
	slot := r.FormValue("slot") // before | after | front | rear | etc.
	if slot == "" {
		slot = kind
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	rel, err := d.Store.Save(headers[0], path.Join("public", tok))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "save failed: "+err.Error())
		return
	}
	// Attach as a vehicle_photo tagged with the slot so the manager sees it immediately.
	_, _ = d.DB.Pool.Exec(r.Context(), `
		INSERT INTO vehicle_photos (company_id, vehicle_id, kind, caption, file_path)
		VALUES ($1,$2,$3,$4,$5)
	`, companyID, vehID, slot, "customer upload for booking "+resID.String()[:8], rel)
	// Mark used (last-write).
	_, _ = d.DB.Pool.Exec(r.Context(), `UPDATE photo_upload_tokens SET used_at=NOW() WHERE token=$1`, tok)

	writeJSON(w, http.StatusOK, map[string]any{"url": "/uploads/" + rel})
}

// --- Checkin / Checkout (manager) ---

type checkReq struct {
	ReservationID string `json:"reservationId"`
	OdometerKm    int    `json:"odometerKm"`
	FuelPct       int    `json:"fuelPct"`
	Note          string `json:"note"`
}

// Checkout: vehicle leaves the shop. Required: odometer + fuel.
func (d *Deps) Checkout(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req checkReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ReservationID == "" || req.OdometerKm <= 0 {
		writeErr(w, http.StatusBadRequest, "reservationId and odometerKm required")
		return
	}
	if req.FuelPct < 0 || req.FuelPct > 100 {
		writeErr(w, http.StatusBadRequest, "fuelPct must be 0..100")
		return
	}
	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	var vehID uuid.UUID
	err = tx.QueryRow(r.Context(), `
		UPDATE reservations
		   SET status='checked_out',
		       checkout_odometer_km=$1, checkout_fuel_pct=$2, checkout_at=NOW()
		 WHERE company_id=$3 AND id=$4
		 RETURNING vehicle_id
	`, req.OdometerKm, req.FuelPct, c.CompanyID, req.ReservationID).Scan(&vehID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "update failed: "+err.Error())
		return
	}
	_, _ = tx.Exec(r.Context(), `UPDATE vehicles SET status='rented', odometer_km=$1 WHERE id=$2`, req.OdometerKm, vehID)
	_, _ = tx.Exec(r.Context(), `
		INSERT INTO mileage_records (company_id, vehicle_id, reservation_id, kind, odometer_km, fuel_level_pct, recorded_by, note)
		VALUES ($1,$2,$3::uuid,'out',$4,$5,$6,NULLIF($7,''))
	`, c.CompanyID, vehID, req.ReservationID, req.OdometerKm, req.FuelPct, c.UserID, req.Note)

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Checkin: vehicle returned. Required: odometer + fuel. Computes extra-km and fuel-delta.
func (d *Deps) Checkin(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req checkReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ReservationID == "" || req.OdometerKm <= 0 {
		writeErr(w, http.StatusBadRequest, "reservationId and odometerKm required")
		return
	}
	if req.FuelPct < 0 || req.FuelPct > 100 {
		writeErr(w, http.StatusBadRequest, "fuelPct must be 0..100")
		return
	}
	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	var vehID uuid.UUID
	var startOdo *int
	err = tx.QueryRow(r.Context(), `
		UPDATE reservations
		   SET status='returned',
		       checkin_odometer_km=$1, checkin_fuel_pct=$2, checkin_at=NOW()
		 WHERE company_id=$3 AND id=$4
		 RETURNING vehicle_id, checkout_odometer_km
	`, req.OdometerKm, req.FuelPct, c.CompanyID, req.ReservationID).Scan(&vehID, &startOdo)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "update failed: "+err.Error())
		return
	}
	_, _ = tx.Exec(r.Context(), `UPDATE vehicles SET status='inspection_pending', odometer_km=$1 WHERE id=$2`, req.OdometerKm, vehID)
	_, _ = tx.Exec(r.Context(), `
		INSERT INTO mileage_records (company_id, vehicle_id, reservation_id, kind, odometer_km, fuel_level_pct, recorded_by, note)
		VALUES ($1,$2,$3::uuid,'in',$4,$5,$6,NULLIF($7,''))
	`, c.CompanyID, vehID, req.ReservationID, req.OdometerKm, req.FuelPct, c.UserID, req.Note)

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	distance := 0
	if startOdo != nil {
		distance = req.OdometerKm - *startOdo
		if distance < 0 {
			distance = 0
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "distanceKm": distance})
}
