package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/mail"
	"github.com/rentalsoftware/backend/internal/storage"
)

type Handler struct {
	Pool       *pgxpool.Pool
	Store      *storage.Store
	Tmpl       *Templates
	Secret     []byte
	PublicBase string
	Mailer     *mail.Sender
	EnvSMTP    mail.Config
}

func New(pool *pgxpool.Pool, store *storage.Store, secret []byte, publicBase string) (*Handler, error) {
	t, err := Load()
	if err != nil {
		return nil, err
	}
	return &Handler{Pool: pool, Store: store, Tmpl: t, Secret: secret, PublicBase: publicBase}, nil
}

// render adds Session + Title + Active into the data and executes the base template.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, title string, active string, data map[string]any) {
	t := h.Tmpl.Lookup(name)
	if t == nil {
		http.Error(w, "template not found: "+name, 500)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["Title"] = title
	data["Active"] = active
	if s, ok := From(r); ok {
		data["Session"] = s
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// companyID returns the one company id for this install (single-tenant mode).
func (h *Handler) companyID(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := h.Pool.QueryRow(ctx, `SELECT id FROM companies ORDER BY created_at LIMIT 1`).Scan(&id)
	return id, err
}

func rand16() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func rentalDays(a, b time.Time) int {
	d := int(math.Ceil(b.Sub(a).Hours() / 24.0))
	if d < 1 {
		d = 1
	}
	return d
}

// =============================================================================
// PUBLIC
// =============================================================================

func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	type branch struct{ ID uuid.UUID; Name string }
	branches := []branch{}
	cid, _ := h.companyID(r.Context())
	if cid != uuid.Nil {
		rows, _ := h.Pool.Query(r.Context(), `SELECT id, name FROM branches WHERE company_id=$1 AND is_active ORDER BY name`, cid)
		if rows != nil {
			for rows.Next() {
				var b branch
				if err := rows.Scan(&b.ID, &b.Name); err == nil {
					branches = append(branches, b)
				}
			}
			rows.Close()
		}
	}
	now := time.Now().Add(2 * time.Hour).Truncate(time.Hour)
	later := now.Add(2 * 24 * time.Hour)
	h.render(w, r, "public/home.html", "mosrental", "", map[string]any{
		"Branches":    branches,
		"DefaultFrom": now.Format("2006-01-02T15:04"),
		"DefaultTo":   later.Format("2006-01-02T15:04"),
	})
}

type vehicleRow struct {
	ID             uuid.UUID
	Make, Model    string
	Year           int
	Seats          *int
	Transmission   *string
	FuelType       *string
	DailyRateCents int
	DepositCents   int
	CompanyName    string
	CompanySlug    string
	PrimaryPhoto   string
}

func parseLocal(s string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	data := map[string]any{"From": fromStr, "To": toStr}
	if fromStr == "" || toStr == "" {
		h.render(w, r, "public/search.html", "Browse cars", "", data)
		return
	}
	from, err1 := parseLocal(fromStr)
	to, err2 := parseLocal(toStr)
	if err1 != nil || err2 != nil || !to.After(from) {
		data["Err"] = "Please pick a valid pickup and return date."
		h.render(w, r, "public/search.html", "Browse cars", "", data)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT v.id, COALESCE(v.make,''), COALESCE(v.model,''), COALESCE(v.year,0),
		       v.seats, v.transmission, v.fuel_type,
		       v.daily_rate_cents, v.deposit_cents,
		       c.name, c.slug,
		       COALESCE((SELECT file_path FROM vehicle_photos WHERE vehicle_id=v.id ORDER BY is_primary DESC, display_order ASC LIMIT 1), '')
		FROM vehicles v JOIN companies c ON c.id=v.company_id
		WHERE c.status='active' AND v.status IN ('available','reserved')
		  AND NOT EXISTS (
		    SELECT 1 FROM reservations r WHERE r.vehicle_id=v.id
		      AND r.status IN ('pending','confirmed','checked_out','awaiting_pickup')
		      AND tstzrange(r.pickup_at, r.return_at, '[)') && tstzrange($1,$2,'[)')
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM blocked_periods b WHERE b.vehicle_id=v.id
		      AND tstzrange(b.starts_at, b.ends_at, '[)') && tstzrange($1,$2,'[)')
		  )
		ORDER BY v.daily_rate_cents ASC LIMIT 100
	`, from, to)
	if err != nil {
		data["Err"] = err.Error()
		h.render(w, r, "public/search.html", "Browse cars", "", data)
		return
	}
	defer rows.Close()
	out := []vehicleRow{}
	for rows.Next() {
		var v vehicleRow
		var photo string
		if err := rows.Scan(&v.ID, &v.Make, &v.Model, &v.Year, &v.Seats, &v.Transmission, &v.FuelType,
			&v.DailyRateCents, &v.DepositCents, &v.CompanyName, &v.CompanySlug, &photo); err == nil {
			if photo != "" {
				v.PrimaryPhoto = "/uploads/" + photo
			}
			out = append(out, v)
		}
	}
	data["Results"] = out
	data["Searched"] = true
	h.render(w, r, "public/search.html", "Available cars", "", data)
}

type bookVehicle struct {
	ID             uuid.UUID
	Make, Model    string
	Year           int
	DailyRateCents int
	DepositCents   int
	CompanyName    string
}

func (h *Handler) BookGet(w http.ResponseWriter, r *http.Request) {
	vid := r.URL.Query().Get("v")
	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")
	from, err1 := parseLocal(fromStr)
	to, err2 := parseLocal(toStr)
	if vid == "" || err1 != nil || err2 != nil || !to.After(from) {
		http.Redirect(w, r, "/search", http.StatusSeeOther)
		return
	}
	var v bookVehicle
	err := h.Pool.QueryRow(r.Context(), `
		SELECT v.id, COALESCE(v.make,''), COALESCE(v.model,''), COALESCE(v.year,0),
		       v.daily_rate_cents, v.deposit_cents, c.name
		FROM vehicles v JOIN companies c ON c.id=v.company_id WHERE v.id=$1
	`, vid).Scan(&v.ID, &v.Make, &v.Model, &v.Year, &v.DailyRateCents, &v.DepositCents, &v.CompanyName)
	if err != nil {
		http.Redirect(w, r, "/search", http.StatusSeeOther)
		return
	}
	days := rentalDays(from, to)
	type addon struct{ Code, Name string; Description *string; PriceCents int; PricePer string }
	addons := []addon{}
	type ins struct{ Code, Name string; Description *string; PricePerDayCents, DeductibleCents int; IsDefault bool }
	insurance := []ins{}
	cid, _ := h.companyID(r.Context())
	if cid != uuid.Nil {
		rows, _ := h.Pool.Query(r.Context(), `SELECT code,name,description,price_cents,price_per FROM addon_catalog WHERE company_id=$1 AND is_active ORDER BY display_order,name`, cid)
		if rows != nil {
			for rows.Next() {
				var a addon
				_ = rows.Scan(&a.Code, &a.Name, &a.Description, &a.PriceCents, &a.PricePer)
				addons = append(addons, a)
			}
			rows.Close()
		}
		rows, _ = h.Pool.Query(r.Context(), `SELECT code,name,description,price_per_day_cents,deductible_cents,is_default FROM insurance_catalog WHERE company_id=$1 AND is_active ORDER BY price_per_day_cents`, cid)
		if rows != nil {
			for rows.Next() {
				var i ins
				_ = rows.Scan(&i.Code, &i.Name, &i.Description, &i.PricePerDayCents, &i.DeductibleCents, &i.IsDefault)
				insurance = append(insurance, i)
			}
			rows.Close()
		}
	}
	// Compute price with rate-period override + tax so the quote matches what the server books.
	vUUID, _ := uuid.Parse(vid)
	pricing, perr := h.priceFor(r.Context(), cid, vUUID, from, to)
	if perr != nil {
		pricing = PricingBreakdown{DailyRateCents: v.DailyRateCents, Days: days, SubtotalCents: v.DailyRateCents * days, TotalCents: v.DailyRateCents * days, TaxLabel: "Tax"}
	}
	// Reflect the effective daily rate (may be from a rate period) in the summary card.
	v.DailyRateCents = pricing.DailyRateCents
	h.render(w, r, "public/book.html", "Checkout", "", map[string]any{
		"Vehicle": v, "From": fromStr, "To": toStr,
		"FromHuman": from.Format("Mon 02 Jan, 15:04"),
		"ToHuman":   to.Format("Mon 02 Jan, 15:04"),
		"Days": pricing.Days, "Base": pricing.SubtotalCents,
		"TaxCents": pricing.TaxCents, "TaxLabel": pricing.TaxLabel, "TaxRatePct": pricing.TaxRatePct,
		"Total":   pricing.TotalCents,
		"Addons": addons, "Insurance": insurance,
	})
}

func licensePlausible(license string) bool {
	l := strings.ToUpper(strings.TrimSpace(license))
	if len(l) < 6 || len(l) > 20 {
		return false
	}
	for _, c := range l {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	if strings.Contains(l, "TEST") || strings.Contains(l, "FAKE") || strings.Contains(l, "SAMPLE") {
		return false
	}
	seen := map[rune]struct{}{}
	for _, c := range l {
		seen[c] = struct{}{}
	}
	return len(seen) >= 3
}

func (h *Handler) BookPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	vid := r.FormValue("vehicleId")
	fromStr := r.FormValue("from")
	toStr := r.FormValue("to")
	from, err1 := parseLocal(fromStr)
	to, err2 := parseLocal(toStr)
	first := strings.TrimSpace(r.FormValue("firstName"))
	last := strings.TrimSpace(r.FormValue("lastName"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	phone := r.FormValue("phone")
	license := strings.ToUpper(strings.TrimSpace(r.FormValue("licenseNumber")))
	password := r.FormValue("password")

	if vid == "" || err1 != nil || err2 != nil || !to.After(from) || first == "" || last == "" || email == "" || license == "" {
		http.Redirect(w, r, "/search", http.StatusSeeOther)
		return
	}
	if !licensePlausible(license) {
		http.Redirect(w, r, fmt.Sprintf("/book?v=%s&from=%s&to=%s&err=1", vid, fromStr, toStr), http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer tx.Rollback(ctx)

	var cid uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT v.company_id FROM vehicles v JOIN companies c ON c.id=v.company_id
		WHERE v.id=$1 AND c.status='active'
	`, vid).Scan(&cid)
	if err != nil {
		http.Error(w, "vehicle unavailable", 400)
		return
	}

	// Find or create customer.
	var custID string
	_ = tx.QueryRow(ctx, `SELECT id FROM customers WHERE company_id=$1 AND LOWER(email)=$2 LIMIT 1`, cid, email).Scan(&custID)
	if custID == "" {
		err = tx.QueryRow(ctx, `
			INSERT INTO customers (company_id, first_name, last_name, email, phone, license_number)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),$6) RETURNING id
		`, cid, first, last, email, phone, license).Scan(&custID)
		if err != nil {
			http.Error(w, "customer create failed", 500)
			return
		}
	} else {
		_, _ = tx.Exec(ctx, `UPDATE customers SET license_number=$1 WHERE id=$2`, license, custID)
	}

	// Optional portal account.
	var sess *Session
	if len(password) >= 8 {
		hash, _ := auth.HashPassword(password)
		var uid uuid.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO users (company_id, email, password_hash, name, role, is_active, email_verified_at)
			VALUES ($1,$2,$3,$4,'customer',TRUE,NOW())
			ON CONFLICT (company_id, email) DO UPDATE SET password_hash=EXCLUDED.password_hash
			RETURNING id
		`, cid, email, hash, first+" "+last).Scan(&uid)
		if err == nil {
			_, _ = tx.Exec(ctx, `UPDATE customers SET user_id=$1 WHERE id=$2`, uid, custID)
			s := Session{UserID: uid, CompanyID: &cid, Role: "customer", Name: first + " " + last}
			sess = &s
		}
	}

	// Server-side pricing: picks the applicable rate period (if any) and applies company tax.
	vUUID, _ := uuid.Parse(vid)
	pricing, err := h.priceFor(ctx, cid, vUUID, from, to)
	if err != nil {
		http.Error(w, "pricing error: "+err.Error(), 500)
		return
	}

	var bookingNumber, resID string
	err = tx.QueryRow(ctx, `
		INSERT INTO reservations (company_id, customer_id, vehicle_id, pickup_at, return_at, status,
		  daily_rate_cents, rental_days, subtotal_cents, tax_cents, total_cents, source)
		VALUES ($1,$2::uuid,$3::uuid,$4,$5,'pending',$6,$7,$8,$9,$10,'customer')
		RETURNING id, booking_number
	`, cid, custID, vid, from, to, pricing.DailyRateCents, pricing.Days,
		pricing.SubtotalCents, pricing.TaxCents, pricing.TotalCents).Scan(&resID, &bookingNumber)
	if err != nil {
		if strings.Contains(err.Error(), "no_overlap") {
			http.Error(w, "That car was just booked for those dates. Pick another one.", 409)
			return
		}
		http.Error(w, "booking failed: "+err.Error(), 500)
		return
	}

	// Persist license verification stub.
	_, _ = tx.Exec(ctx, `
		INSERT INTO license_verifications (company_id, customer_id, reservation_id, license_number, checksum_valid, automated_note, manual_status)
		VALUES ($1,$2::uuid,$3::uuid,$4,TRUE,'automated plausibility check passed','pending')
	`, cid, custID, resID, license)

	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "commit failed", 500)
		return
	}
	// Pre-invoice (best-effort — never breaks the booking).
	if rid, err := uuid.Parse(resID); err == nil {
		if _, ierr := h.EnsurePreInvoice(ctx, cid, rid); ierr != nil {
			log.Printf("pre-invoice: %v", ierr)
		}
	}
	if sess != nil {
		_ = SetCookie(w, h.Secret, *sess, 24*30)
	}
	// Confirmation page.
	h.render(w, r, "public/confirm.html", "Booking confirmed", "", map[string]any{
		"BookingNumber": bookingNumber,
		"TotalCents":    pricing.TotalCents,
	})
}

func (h *Handler) PortalLoginGet(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/portal_login.html", "Sign in", "", nil)
}
func (h *Handler) PortalLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	var id uuid.UUID
	var cid *uuid.UUID
	var hash, role, name string
	var active bool
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, company_id, password_hash, role, name, is_active
		FROM users WHERE email=$1 AND role='customer' LIMIT 1
	`, email).Scan(&id, &cid, &hash, &role, &name, &active)
	if err != nil || !active || !auth.VerifyPassword(hash, pw) {
		h.render(w, r, "public/portal_login.html", "Sign in", "", map[string]any{"Err": "Invalid email or password."})
		return
	}
	_ = SetCookie(w, h.Secret, Session{UserID: id, CompanyID: cid, Role: role, Name: name}, 24*30)
	http.Redirect(w, r, "/portal", http.StatusSeeOther)
}
func (h *Handler) PortalLogoutPost(w http.ResponseWriter, r *http.Request) {
	ClearCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) Portal(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	type row struct {
		ID            uuid.UUID
		BookingNumber string
		Status        string
		PickupAt      time.Time
		ReturnAt      time.Time
		TotalCents    int
		Vehicle       string
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at, r.total_cents,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'')
		FROM reservations r
		JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE cu.user_id=$1 ORDER BY r.pickup_at DESC
	`, s.UserID)
	bookings := []row{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b row
			if err := rows.Scan(&b.ID, &b.BookingNumber, &b.Status, &b.PickupAt, &b.ReturnAt, &b.TotalCents, &b.Vehicle); err == nil {
				bookings = append(bookings, b)
			}
		}
	}
	h.render(w, r, "public/portal.html", "My bookings", "", map[string]any{"Bookings": bookings})
}

// Public photo-upload page.
type uploadInfo struct {
	Kind, BookingNumber, Vehicle, Customer string
}
type uploadSlot struct{ Slot, Label string }

func (h *Handler) UploadGet(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var info uploadInfo
	var exp time.Time
	err := h.Pool.QueryRow(r.Context(), `
		SELECT pt.kind, pt.expires_at, r.booking_number,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       COALESCE(cu.first_name||' '||cu.last_name,'')
		FROM photo_upload_tokens pt
		JOIN reservations r ON r.id=pt.reservation_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE pt.token=$1
	`, tok).Scan(&info.Kind, &exp, &info.BookingNumber, &info.Vehicle, &info.Customer)
	if err != nil || time.Now().After(exp) {
		h.render(w, r, "public/upload.html", "Link invalid", "", map[string]any{"Err": "This upload link is invalid or expired."})
		return
	}
	slots := []uploadSlot{}
	if info.Kind == "before" || info.Kind == "both" {
		slots = append(slots,
			uploadSlot{"before_front", "Front — before"},
			uploadSlot{"before_rear", "Rear — before"},
			uploadSlot{"before_left", "Left — before"},
			uploadSlot{"before_right", "Right — before"},
			uploadSlot{"before_interior", "Interior — before"},
			uploadSlot{"before_odometer", "Odometer — before"},
		)
	}
	if info.Kind == "after" || info.Kind == "both" {
		slots = append(slots,
			uploadSlot{"after_front", "Front — after"},
			uploadSlot{"after_rear", "Rear — after"},
			uploadSlot{"after_left", "Left — after"},
			uploadSlot{"after_right", "Right — after"},
			uploadSlot{"after_interior", "Interior — after"},
			uploadSlot{"after_odometer", "Odometer — after"},
		)
	}
	flash := r.URL.Query().Get("flash")
	h.render(w, r, "public/upload.html", "Upload photos", "", map[string]any{
		"Info": info, "Token": tok, "Slots": slots, "Flash": flash,
	})
}

func (h *Handler) UploadPost(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	var cid, resID, vehID uuid.UUID
	var exp time.Time
	err := h.Pool.QueryRow(r.Context(), `
		SELECT pt.company_id, pt.reservation_id, r.vehicle_id, pt.expires_at
		FROM photo_upload_tokens pt JOIN reservations r ON r.id=pt.reservation_id
		WHERE pt.token=$1
	`, tok).Scan(&cid, &resID, &vehID, &exp)
	if err != nil || time.Now().After(exp) {
		http.Error(w, "invalid or expired link", 410)
		return
	}
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Error(w, "multipart parse", 400)
		return
	}
	slot := r.FormValue("slot")
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Error(w, "file required", 400)
		return
	}
	rel, err := h.Store.Save(headers[0], path.Join("public", tok))
	if err != nil {
		http.Error(w, "save failed: "+err.Error(), 400)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO vehicle_photos (company_id, vehicle_id, kind, caption, file_path)
		VALUES ($1,$2,$3,$4,$5)
	`, cid, vehID, slot, "customer upload for "+resID.String()[:8], rel)
	_, _ = h.Pool.Exec(r.Context(), `UPDATE photo_upload_tokens SET used_at=NOW() WHERE token=$1`, tok)
	http.Redirect(w, r, "/upload/"+tok+"?flash=Uploaded", http.StatusSeeOther)
}

// =============================================================================
// ADMIN
// =============================================================================

func (h *Handler) AdminLoginGet(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/admin"
	}
	h.render(w, r, "admin/login.html", "Admin sign in", "", map[string]any{"Next": next})
}
func (h *Handler) AdminLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	next := r.FormValue("next")
	if next == "" {
		next = "/admin"
	}
	var id uuid.UUID
	var cid *uuid.UUID
	var hash, role, name string
	var active bool
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, company_id, password_hash, role, name, is_active FROM users
		WHERE email=$1 ORDER BY (company_id IS NULL) DESC LIMIT 1
	`, email).Scan(&id, &cid, &hash, &role, &name, &active)
	if err != nil || !active || !auth.VerifyPassword(hash, pw) {
		h.render(w, r, "admin/login.html", "Admin sign in", "", map[string]any{"Err": "Invalid credentials.", "Next": next})
		return
	}
	sess := Session{UserID: id, CompanyID: cid, Role: role, Name: name}
	if !sess.IsAdmin() {
		h.render(w, r, "admin/login.html", "Admin sign in", "", map[string]any{"Err": "This account doesn't have staff access.", "Next": next})
		return
	}
	_ = SetCookie(w, h.Secret, sess, 12)
	http.Redirect(w, r, next, http.StatusSeeOther)
}
func (h *Handler) AdminLogout(w http.ResponseWriter, _ *http.Request) {
	ClearCookie(w)
}
func (h *Handler) AdminLogoutRedirect(w http.ResponseWriter, r *http.Request) {
	ClearCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *Handler) SetupGet(w http.ResponseWriter, r *http.Request) {
	var n int
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM users WHERE company_id IS NULL AND role='platform_owner'`).Scan(&n)
	if n > 0 {
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return
	}
	h.render(w, r, "admin/setup.html", "First-time setup", "", nil)
}

func (h *Handler) SetupPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	companyName := strings.TrimSpace(r.FormValue("companyName"))
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	pw := r.FormValue("password")
	if companyName == "" || name == "" || email == "" || len(pw) < 8 {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": "All fields required; password ≥ 8 chars."})
		return
	}
	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": err.Error()})
		return
	}
	defer tx.Rollback(ctx)
	var n int
	_ = tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role='platform_owner'`).Scan(&n)
	if n > 0 {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": "Admin already exists."})
		return
	}
	hash, _ := auth.HashPassword(pw)
	// Create the one company + a company_owner.
	slug := strings.ToLower(strings.ReplaceAll(companyName, " ", "-"))
	if len(slug) < 3 {
		slug = "company"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	var cid uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO companies (name, slug, status) VALUES ($1,$2,'active') RETURNING id`, companyName, slug).Scan(&cid)
	if err != nil {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": err.Error()})
		return
	}
	var uid uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO users (company_id, email, password_hash, name, role, is_active, email_verified_at)
		VALUES ($1,$2,$3,$4,'company_owner',TRUE,NOW()) RETURNING id
	`, cid, email, hash, name).Scan(&uid)
	if err != nil {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": err.Error()})
		return
	}
	// Seed default settings + catalog (matches migration triggers for existing companies).
	_, _ = tx.Exec(ctx, `
		INSERT INTO settings (company_id, key, value)
		VALUES ($1,'general', jsonb_build_object('currency','USD','timezone','UTC','mileageUnit','km','businessName',$2))
		ON CONFLICT DO NOTHING
	`, cid, companyName)
	_, _ = tx.Exec(ctx, `INSERT INTO addon_catalog (company_id, code, name, description, price_cents, price_per, display_order)
		VALUES ($1,'child_seat','Child seat','Rear-facing child seat',500,'day',1),
		       ($1,'booster','Booster seat','For older kids',400,'day',2),
		       ($1,'gps','GPS unit','Portable GPS',300,'day',3),
		       ($1,'extra_driver','Additional driver','Add a second driver',1500,'rental',4),
		       ($1,'wifi','In-car Wi-Fi','Portable 4G hotspot',700,'day',5)
		ON CONFLICT DO NOTHING`, cid)
	_, _ = tx.Exec(ctx, `INSERT INTO insurance_catalog (company_id, code, name, description, price_per_day_cents, deductible_cents, is_default)
		VALUES ($1,'none','Decline','I have my own coverage',0,0,FALSE),
		       ($1,'basic','Basic protection','$1,000 deductible',1500,100000,TRUE),
		       ($1,'premium','Premium zero-deductible','Zero deductible',2900,0,FALSE)
		ON CONFLICT DO NOTHING`, cid)
	_, _ = tx.Exec(ctx, `INSERT INTO branches (company_id, name, timezone) VALUES ($1,'Main Branch','UTC') ON CONFLICT DO NOTHING`, cid)
	if err := tx.Commit(ctx); err != nil {
		h.render(w, r, "admin/setup.html", "Setup", "", map[string]any{"Err": err.Error()})
		return
	}
	_ = SetCookie(w, h.Secret, Session{UserID: uid, CompanyID: &cid, Role: "company_owner", Name: name}, 12)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	stats := map[string]int{}
	queries := map[string]string{
		"fleetSize":   `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`,
		"available":   `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='available'`,
		"rented":      `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='rented'`,
		"maintenance": `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status IN ('maintenance','maintenance_required')`,
	}
	for k, q := range queries {
		var n int
		_ = h.Pool.QueryRow(r.Context(), q, cid).Scan(&n)
		stats[k] = n
	}
	var todayPick, todayRet, overdue, pending int
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND pickup_at::date = CURRENT_DATE`, cid).Scan(&todayPick)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND return_at::date = CURRENT_DATE`, cid).Scan(&todayRet)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='checked_out' AND return_at < NOW()`, cid).Scan(&overdue)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='pending'`, cid).Scan(&pending)

	reports := h.reportsData(r.Context(), cid)
	alerts := h.fleetAlerts(r.Context(), cid)

	h.render(w, r, "admin/dashboard.html", "Dashboard", "dashboard", map[string]any{
		"Stats": stats, "TodayPickups": todayPick, "TodayReturns": todayRet,
		"Overdue": overdue, "Pending": pending, "Reports": reports, "Alerts": alerts,
		"Now": time.Now().Format("Mon 02 Jan 15:04"),
	})
}

func (h *Handler) reportsData(ctx context.Context, cid uuid.UUID) map[string]int {
	out := map[string]int{}
	_ = h.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE company_id=$1 AND status='succeeded'`, cid).Scan(new(int))
	for k, q := range map[string]string{
		"revenueCents":     `SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE company_id=$1 AND status='succeeded'`,
		"outstandingCents": `SELECT COALESCE(SUM(GREATEST(total_cents-paid_cents,0)),0) FROM reservations WHERE company_id=$1 AND status IN ('pending','confirmed','checked_out')`,
		"bookings":         `SELECT COUNT(*) FROM reservations WHERE company_id=$1`,
		"cancelled":        `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='cancelled'`,
		"maintenanceCents": `SELECT COALESCE(SUM(cost_cents),0) FROM maintenance_records WHERE company_id=$1`,
		"fleet":            `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`,
	} {
		var n int
		_ = h.Pool.QueryRow(ctx, q, cid).Scan(&n)
		out[k] = n
	}
	var fleet, available int
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, cid).Scan(&fleet)
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='available'`, cid).Scan(&available)
	if fleet > 0 {
		out["utilizationPct"] = 100 * (fleet - available) / fleet
	}
	return out
}

type Alert struct{ VehicleID uuid.UUID; Vehicle, Message string }

func (h *Handler) fleetAlerts(ctx context.Context, cid uuid.UUID) []Alert {
	out := []Alert{}
	rows, err := h.Pool.Query(ctx, `
		SELECT id, COALESCE(year::text,'')||' '||COALESCE(make,'')||' '||COALESCE(model,'') AS label,
		       registration_expiry, insurance_expiry, next_oil_change_date
		FROM vehicles WHERE company_id=$1
	`, cid)
	if err != nil {
		return out
	}
	defer rows.Close()
	today := time.Now()
	soon := today.Add(14 * 24 * time.Hour)
	for rows.Next() {
		var id uuid.UUID
		var label string
		var reg, ins, oil *time.Time
		if err := rows.Scan(&id, &label, &reg, &ins, &oil); err != nil {
			continue
		}
		for name, exp := range map[string]*time.Time{"registration": reg, "insurance": ins, "oil change": oil} {
			if exp == nil {
				continue
			}
			if exp.Before(today) {
				out = append(out, Alert{id, label, name + " expired " + exp.Format("2006-01-02")})
			} else if exp.Before(soon) {
				out = append(out, Alert{id, label, name + " expires " + exp.Format("2006-01-02")})
			}
		}
	}
	return out
}

// -- Reservations --

type ReservationLite2 struct {
	ID            uuid.UUID
	BookingNumber string
	Status        string
	CustomerName  *string
	VehicleLabel  *string
	PickupAt      time.Time
	ReturnAt      time.Time
	TotalCents    int
}

func (h *Handler) Reservations(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "all"
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	flash := r.URL.Query().Get("flash")

	tabCount := func(w string, args ...any) int {
		var n int
		_ = h.Pool.QueryRow(r.Context(), w, args...).Scan(&n)
		return n
	}
	type tabDef struct{ Key, Label string; Count int }
	tabs := []tabDef{
		{"all", "All", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1`, cid)},
		{"today_pickups", "Today's pickups", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND pickup_at::date=CURRENT_DATE`, cid)},
		{"today_returns", "Today's returns", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND return_at::date=CURRENT_DATE`, cid)},
		{"tomorrow_pickups", "Tomorrow's pickups", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND pickup_at::date=CURRENT_DATE+1`, cid)},
		{"on_rent", "On rent", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='checked_out'`, cid)},
		{"completed", "Completed", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='returned'`, cid)},
		{"cancelled", "Cancelled", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='cancelled'`, cid)},
		{"outstanding", "Outstanding", tabCount(`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND total_cents > paid_cents AND status NOT IN ('cancelled','no_show')`, cid)},
	}

	where := "r.company_id=$1"
	args := []any{cid}
	switch tab {
	case "today_pickups":
		where += " AND r.pickup_at::date=CURRENT_DATE"
	case "today_returns":
		where += " AND r.return_at::date=CURRENT_DATE"
	case "tomorrow_pickups":
		where += " AND r.pickup_at::date=CURRENT_DATE+1"
	case "on_rent":
		where += " AND r.status='checked_out'"
	case "completed":
		where += " AND r.status='returned'"
	case "cancelled":
		where += " AND r.status='cancelled'"
	case "outstanding":
		where += " AND r.total_cents > r.paid_cents AND r.status NOT IN ('cancelled','no_show')"
	}
	if q != "" {
		args = append(args, "%"+q+"%")
		where += fmt.Sprintf(" AND (r.booking_number ILIKE $%d OR cu.email ILIKE $%d OR cu.first_name||' '||cu.last_name ILIKE $%d OR v.license_plate ILIKE $%d)", len(args), len(args), len(args), len(args))
	}
	sql := `
		SELECT r.id, r.booking_number, r.status,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE cu.first_name||' '||cu.last_name END,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       r.pickup_at, r.return_at, r.total_cents
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE ` + where + ` ORDER BY r.pickup_at DESC LIMIT 500`
	rows, err := h.Pool.Query(r.Context(), sql, args...)
	list := []ReservationLite2{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rr ReservationLite2
			if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Status, &rr.CustomerName, &rr.VehicleLabel, &rr.PickupAt, &rr.ReturnAt, &rr.TotalCents); err == nil {
				list = append(list, rr)
			}
		}
	}
	h.render(w, r, "admin/reservations.html", "Reservations", "reservations", map[string]any{
		"Reservations": list, "Tabs": tabs, "Tab": tab, "Q": q, "Flash": flash,
	})
}

type ReservationDetail struct {
	ID             uuid.UUID
	BookingNumber  string
	Status         string
	CustomerName   *string
	VehicleLabel   *string
	PickupAt       time.Time
	ReturnAt       time.Time
	DailyRateCents int
	RentalDays     int
	SubtotalCents  int
	ExtrasCents    int
	TaxCents       int
	TotalCents     int
	PaidCents      int
}

type Hist struct{ CreatedAt time.Time; FromStatus *string; ToStatus string }

func (h *Handler) ReservationDetailPage(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	var d ReservationDetail
	err := h.Pool.QueryRow(r.Context(), `
		SELECT r.id, r.booking_number, r.status,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE cu.first_name||' '||cu.last_name END,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       r.pickup_at, r.return_at, r.daily_rate_cents, r.rental_days,
		       r.subtotal_cents, r.extras_cents, r.tax_cents, r.total_cents, r.paid_cents
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, id).Scan(&d.ID, &d.BookingNumber, &d.Status, &d.CustomerName, &d.VehicleLabel,
		&d.PickupAt, &d.ReturnAt, &d.DailyRateCents, &d.RentalDays,
		&d.SubtotalCents, &d.ExtrasCents, &d.TaxCents, &d.TotalCents, &d.PaidCents)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	hrows, _ := h.Pool.Query(r.Context(), `SELECT created_at, from_status, to_status FROM reservation_status_history WHERE reservation_id=$1 ORDER BY created_at DESC LIMIT 50`, d.ID)
	history := []Hist{}
	if hrows != nil {
		for hrows.Next() {
			var hh Hist
			_ = hrows.Scan(&hh.CreatedAt, &hh.FromStatus, &hh.ToStatus)
			history = append(history, hh)
		}
		hrows.Close()
	}
	invs := h.invoicesForReservation(r.Context(), cid, d.ID)
	pre, post := h.InspectionsForReservation(r, d.ID)
	h.render(w, r, "admin/reservation_detail.html", "Booking "+d.BookingNumber, "reservations", map[string]any{
		"R": d, "History": history, "Invoices": invs,
		"Pre": pre, "Post": post,
		"Flash":     r.URL.Query().Get("flash"),
		"PhotoLink": r.URL.Query().Get("link"),
	})
}

func (h *Handler) ReservationStatus(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	status := r.FormValue("status")
	_, _ = h.Pool.Exec(r.Context(), `UPDATE reservations SET status=$1 WHERE company_id=$2 AND id=$3`, status, cid, id)
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Status+updated", http.StatusSeeOther)
}

func (h *Handler) ReservationCheckout(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	odo, _ := strconv.Atoi(r.FormValue("odometerKm"))
	fuel, _ := strconv.Atoi(r.FormValue("fuelPct"))
	note := r.FormValue("note")
	if odo <= 0 {
		http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Odometer+required#checkout", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, _ := h.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	var vehID uuid.UUID
	err := tx.QueryRow(ctx, `
		UPDATE reservations SET status='checked_out', checkout_odometer_km=$1, checkout_fuel_pct=$2, checkout_at=NOW()
		WHERE company_id=$3 AND id=$4 RETURNING vehicle_id
	`, odo, fuel, cid, id).Scan(&vehID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE vehicles SET status='rented', odometer_km=$1 WHERE id=$2`, odo, vehID)
	_, _ = tx.Exec(ctx, `INSERT INTO mileage_records (company_id,vehicle_id,reservation_id,kind,odometer_km,fuel_level_pct,recorded_by,note)
		VALUES ($1,$2,$3::uuid,'out',$4,$5,$6,NULLIF($7,''))`, cid, vehID, id, odo, fuel, s.UserID, note)
	_ = tx.Commit(ctx)
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Checked+out", http.StatusSeeOther)
}

func (h *Handler) ReservationCheckin(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	odo, _ := strconv.Atoi(r.FormValue("odometerKm"))
	fuel, _ := strconv.Atoi(r.FormValue("fuelPct"))
	note := r.FormValue("note")
	if odo <= 0 {
		http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Odometer+required#checkin", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, _ := h.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	var vehID uuid.UUID
	err := tx.QueryRow(ctx, `
		UPDATE reservations SET status='returned', checkin_odometer_km=$1, checkin_fuel_pct=$2, checkin_at=NOW()
		WHERE company_id=$3 AND id=$4 RETURNING vehicle_id
	`, odo, fuel, cid, id).Scan(&vehID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE vehicles SET status='inspection_pending', odometer_km=$1 WHERE id=$2`, odo, vehID)
	_, _ = tx.Exec(ctx, `INSERT INTO mileage_records (company_id,vehicle_id,reservation_id,kind,odometer_km,fuel_level_pct,recorded_by,note)
		VALUES ($1,$2,$3::uuid,'in',$4,$5,$6,NULLIF($7,''))`, cid, vehID, id, odo, fuel, s.UserID, note)
	_ = tx.Commit(ctx)
	// Spin up the post-rental invoice (admin can tune line items + send).
	if rid, err := uuid.Parse(id); err == nil {
		if _, ierr := h.EnsurePostInvoice(ctx, cid, rid); ierr != nil {
			log.Printf("post-invoice: %v", ierr)
		}
	}
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Checked+in+%E2%80%94+final+invoice+ready", http.StatusSeeOther)
}

func (h *Handler) ReservationPayment(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	amt, _ := strconv.Atoi(r.FormValue("amountCents"))
	method := r.FormValue("method")
	ref := r.FormValue("reference")
	if amt <= 0 {
		http.Redirect(w, r, "/admin/reservations/"+id, http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, _ := h.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	_, err := tx.Exec(ctx, `INSERT INTO payments (company_id, reservation_id, amount_cents, method, status, reference)
		VALUES ($1,$2::uuid,$3,$4,'succeeded',NULLIF($5,''))`, cid, id, amt, method, ref)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, _ = tx.Exec(ctx, `UPDATE reservations SET paid_cents=paid_cents+$1 WHERE company_id=$2 AND id=$3`, amt, cid, id)
	_ = tx.Commit(ctx)
	http.Redirect(w, r, "/admin/reservations/"+id+"?flash=Payment+recorded", http.StatusSeeOther)
}

func (h *Handler) ReservationPhotoLink(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	tok := rand16()
	exp := time.Now().Add(14 * 24 * time.Hour)
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO photo_upload_tokens (token, company_id, reservation_id, kind, expires_at, created_by)
		VALUES ($1,$2,$3::uuid,'both',$4,$5)
	`, tok, cid, id, exp, s.UserID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	link := h.shareBase(r) + "/upload/" + tok
	// Queue a notification.
	var email *string
	_ = h.Pool.QueryRow(r.Context(), `SELECT cu.email FROM reservations r JOIN customers cu ON cu.id=r.customer_id WHERE r.id=$1`, id).Scan(&email)
	recipient := ""
	if email != nil {
		recipient = *email
	}
	_, _ = h.Pool.Exec(r.Context(), `INSERT INTO notifications (company_id, kind, recipient, subject, body, status) VALUES ($1,'photo_upload_link',$2,'Upload your pickup photos',$3,'queued')`,
		cid, recipient, "Please upload your vehicle photos: "+link)
	http.Redirect(w, r, "/admin/reservations/"+id+"?link="+link, http.StatusSeeOther)
}

// -- Vehicles --

type VehicleRow struct {
	ID             uuid.UUID
	FleetNumber    *string
	LicensePlate   *string
	Make, Model    string
	Year           int
	DailyRateCents int
	Status         string
	OdometerKm     *int
}

func (h *Handler) VehiclesList(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, fleet_number, license_plate, COALESCE(make,''), COALESCE(model,''), COALESCE(year,0),
		       daily_rate_cents, status, odometer_km
		FROM vehicles WHERE company_id=$1 ORDER BY created_at DESC LIMIT 500
	`, cid)
	list := []VehicleRow{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v VehicleRow
			_ = rows.Scan(&v.ID, &v.FleetNumber, &v.LicensePlate, &v.Make, &v.Model, &v.Year, &v.DailyRateCents, &v.Status, &v.OdometerKm)
			list = append(list, v)
		}
	}
	h.render(w, r, "admin/vehicles.html", "Fleet", "vehicles", map[string]any{
		"Vehicles": list, "Flash": r.URL.Query().Get("flash"),
	})
}

// FeatureOpts: the fixed menu of selectable vehicle features. Stored on vehicles.features (jsonb).
type FeatureOpt struct{ Key, Label string }

var featureOpts = []FeatureOpt{
	{"ac", "Air conditioning"}, {"bluetooth", "Bluetooth"}, {"usb", "USB ports"},
	{"apple_carplay", "Apple CarPlay"}, {"android_auto", "Android Auto"},
	{"backup_camera", "Backup camera"}, {"parking_sensors", "Parking sensors"},
	{"cruise", "Cruise control"}, {"heated_seats", "Heated seats"},
	{"sunroof", "Sunroof"}, {"leather", "Leather seats"},
	{"roof_rack", "Roof rack"}, {"child_seat_ready", "Child-seat anchors"},
	{"keyless", "Keyless entry"}, {"auto_hold", "Auto hold / parking brake"},
	{"all_wheel_drive", "All-wheel drive"},
}

func (h *Handler) VehicleNewGet(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	type AddonLite struct {
		Code, Name string
		PriceCents int
		PricePer   string
	}
	addons := []AddonLite{}
	rows, _ := h.Pool.Query(r.Context(),
		`SELECT code, name, price_cents, price_per FROM addon_catalog WHERE company_id=$1 AND is_active ORDER BY display_order, name`, cid)
	if rows != nil {
		for rows.Next() {
			var a AddonLite
			_ = rows.Scan(&a.Code, &a.Name, &a.PriceCents, &a.PricePer)
			addons = append(addons, a)
		}
		rows.Close()
	}
	h.render(w, r, "admin/vehicle_new.html", "Add vehicle", "vehicles", map[string]any{
		"FeatureList": featureOpts,
		"Addons":      addons,
	})
}

func (h *Handler) VehicleNewPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	mk := strings.TrimSpace(r.FormValue("make"))
	model := strings.TrimSpace(r.FormValue("model"))
	year, _ := strconv.Atoi(r.FormValue("year"))
	daily, _ := strconv.Atoi(r.FormValue("dailyRateCents"))
	dep, _ := strconv.Atoi(r.FormValue("depositCents"))
	odo, _ := strconv.Atoi(r.FormValue("odometerKm"))
	seats, _ := strconv.Atoi(r.FormValue("seats"))
	fleet := r.FormValue("fleetNumber")
	plate := r.FormValue("licensePlate")
	trans := r.FormValue("transmission")
	fuel := r.FormValue("fuelType")

	if mk == "" || model == "" {
		h.render(w, r, "admin/vehicle_new.html", "Add vehicle", "vehicles", map[string]any{
			"Err": "Make and model required.", "FeatureList": featureOpts,
		})
		return
	}

	// Collect features into a JSON object { ac: true, bluetooth: true, ... }
	features := map[string]bool{}
	for _, v := range r.Form["feature"] {
		features[v] = true
	}
	featJSON, _ := jsonMarshal(features)

	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		h.render(w, r, "admin/vehicle_new.html", "Add vehicle", "vehicles", map[string]any{"Err": err.Error(), "FeatureList": featureOpts})
		return
	}
	defer tx.Rollback(ctx)

	var vid uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO vehicles (company_id, fleet_number, license_plate, make, model, year, transmission, fuel_type, seats, odometer_km, daily_rate_cents, deposit_cents, status, features)
		VALUES ($1,NULLIF($2,''),NULLIF($3,''),$4,$5,NULLIF($6,0),NULLIF($7,''),NULLIF($8,''),NULLIF($9,0),$10,$11,$12,'available',$13::jsonb)
		RETURNING id
	`, cid, fleet, plate, mk, model, year, trans, fuel, seats, odo, daily, dep, string(featJSON)).Scan(&vid)
	if err != nil {
		h.render(w, r, "admin/vehicle_new.html", "Add vehicle", "vehicles", map[string]any{"Err": err.Error(), "FeatureList": featureOpts})
		return
	}

	// Save selected addons as per-vehicle links.
	for _, code := range r.Form["addon"] {
		_, _ = tx.Exec(ctx, `
			INSERT INTO vehicle_addons (company_id, vehicle_id, addon_code) VALUES ($1,$2,$3)
			ON CONFLICT DO NOTHING
		`, cid, vid, code)
	}
	if err := tx.Commit(ctx); err != nil {
		h.render(w, r, "admin/vehicle_new.html", "Add vehicle", "vehicles", map[string]any{"Err": err.Error(), "FeatureList": featureOpts})
		return
	}
	http.Redirect(w, r, "/admin/vehicles/"+vid.String()+"?flash=Vehicle+added", http.StatusSeeOther)
}

// -- Customers --

type CustomerRow struct {
	ID            uuid.UUID
	FirstName     string
	LastName      string
	Email         *string
	Phone         *string
	LicenseNumber *string
	LicenseExpiry *string
}

func (h *Handler) Customers(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, first_name, last_name, email, phone, license_number, license_expiry::text
		FROM customers WHERE company_id=$1
		  AND ($2='' OR first_name ILIKE '%'||$2||'%' OR last_name ILIKE '%'||$2||'%' OR email ILIKE '%'||$2||'%' OR phone ILIKE '%'||$2||'%')
		ORDER BY created_at DESC LIMIT 500
	`, cid, q)
	list := []CustomerRow{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var c CustomerRow
			_ = rows.Scan(&c.ID, &c.FirstName, &c.LastName, &c.Email, &c.Phone, &c.LicenseNumber, &c.LicenseExpiry)
			list = append(list, c)
		}
	}
	h.render(w, r, "admin/customers.html", "Customers", "customers", map[string]any{"Customers": list, "Q": q})
}

// -- Active rentals --

type RentalCard struct {
	ID            uuid.UUID
	BookingNumber string
	Status        string
	Overdue       bool
	PickupAt      time.Time
	ReturnAt      time.Time
	Vehicle       string
	LicensePlate  *string
	CustomerFirst *string
	CustomerLast  *string
}

func (h *Handler) ActiveRentals(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, err := h.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.license_plate, cu.first_name, cu.last_name
		FROM reservations r
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE r.company_id=$1 AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		ORDER BY r.pickup_at ASC
	`, cid)
	list := []RentalCard{}
	now := time.Now()
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rr RentalCard
			_ = rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Status, &rr.PickupAt, &rr.ReturnAt, &rr.Vehicle, &rr.LicensePlate, &rr.CustomerFirst, &rr.CustomerLast)
			rr.Overdue = rr.Status == "checked_out" && rr.ReturnAt.Before(now)
			list = append(list, rr)
		}
	}
	h.render(w, r, "admin/active.html", "Who's got what", "active", map[string]any{"Rentals": list})
}

// -- Licenses --

type LicRow struct {
	ID             uuid.UUID
	Customer       string
	BookingNumber  *string
	LicenseNumber  string
	ExpiresOn      *string
	ChecksumValid  *bool
	ManualStatus   string
}

func (h *Handler) Licenses(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, err := h.Pool.Query(r.Context(), `
		SELECT lv.id,
		       COALESCE(cu.first_name||' '||cu.last_name,'—'),
		       r.booking_number, lv.license_number, lv.expires_on::text,
		       lv.checksum_valid, lv.manual_status
		FROM license_verifications lv
		LEFT JOIN customers cu ON cu.id=lv.customer_id
		LEFT JOIN reservations r ON r.id=lv.reservation_id
		WHERE lv.company_id=$1 ORDER BY lv.created_at DESC LIMIT 200
	`, cid)
	list := []LicRow{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var lr LicRow
			_ = rows.Scan(&lr.ID, &lr.Customer, &lr.BookingNumber, &lr.LicenseNumber, &lr.ExpiresOn, &lr.ChecksumValid, &lr.ManualStatus)
			list = append(list, lr)
		}
	}
	h.render(w, r, "admin/licenses.html", "License review", "licenses", map[string]any{"List": list})
}
func (h *Handler) LicenseReview(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	status := r.FormValue("status")
	if status != "approved" && status != "rejected" {
		http.Redirect(w, r, "/admin/licenses", http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE license_verifications SET manual_status=$1, reviewed_by=$2, reviewed_at=NOW() WHERE company_id=$3 AND id=$4`, status, s.UserID, cid, id)
	http.Redirect(w, r, "/admin/licenses", http.StatusSeeOther)
}

// -- Calendar --

func (h *Handler) Calendar(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	start := time.Now().Truncate(24 * time.Hour)
	days := []time.Time{}
	for i := 0; i < 14; i++ {
		days = append(days, start.Add(time.Duration(i)*24*time.Hour))
	}
	rows, _ := h.Pool.Query(r.Context(), `
		SELECT COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       r.pickup_at, r.return_at, r.status
		FROM reservations r LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.company_id=$1 AND r.return_at > $2 AND r.pickup_at < $3
		ORDER BY r.pickup_at
	`, cid, start, start.Add(14*24*time.Hour))
	groups := map[string][]CalendarEvent{}
	if rows != nil {
		for rows.Next() {
			var veh string
			var e CalendarEvent
			_ = rows.Scan(&veh, &e.PickupAt, &e.ReturnAt, &e.Status)
			groups[veh] = append(groups[veh], e)
		}
		rows.Close()
	}
	h.render(w, r, "admin/calendar.html", "Calendar", "calendar", map[string]any{"Days": days, "Groups": groups})
}

// -- Maintenance --

type MaintRow struct{ ID uuid.UUID; Vehicle, Kind string; Description, Vendor *string; CostCents int; PerformedOn string }

func (h *Handler) Maintenance(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, _ := h.Pool.Query(r.Context(), `
		SELECT m.id, COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       m.kind, m.description, m.cost_cents, m.vendor, m.performed_on::text
		FROM maintenance_records m JOIN vehicles v ON v.id=m.vehicle_id
		WHERE m.company_id=$1 ORDER BY m.performed_on DESC LIMIT 200
	`, cid)
	list := []MaintRow{}
	if rows != nil {
		for rows.Next() {
			var m MaintRow
			_ = rows.Scan(&m.ID, &m.Vehicle, &m.Kind, &m.Description, &m.CostCents, &m.Vendor, &m.PerformedOn)
			list = append(list, m)
		}
		rows.Close()
	}
	vrows, _ := h.Pool.Query(r.Context(), `SELECT id, COALESCE(year,0), COALESCE(make,''), COALESCE(model,'') FROM vehicles WHERE company_id=$1 ORDER BY make,model`, cid)
	type vopt struct{ ID uuid.UUID; Year int; Make, Model string }
	vehicles := []vopt{}
	if vrows != nil {
		for vrows.Next() {
			var v vopt
			_ = vrows.Scan(&v.ID, &v.Year, &v.Make, &v.Model)
			vehicles = append(vehicles, v)
		}
		vrows.Close()
	}
	h.render(w, r, "admin/maintenance.html", "Maintenance", "maintenance", map[string]any{
		"Records": list, "Vehicles": vehicles, "Today": time.Now().Format("2006-01-02"),
		"Flash": r.URL.Query().Get("flash"),
	})
}
func (h *Handler) MaintenancePost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	vid := r.FormValue("vehicleId")
	kind := r.FormValue("kind")
	if kind == "" {
		kind = "service"
	}
	desc := r.FormValue("description")
	cost, _ := strconv.Atoi(r.FormValue("costCents"))
	vendor := r.FormValue("vendor")
	when := r.FormValue("performedOn")
	if when == "" {
		when = time.Now().Format("2006-01-02")
	}
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO maintenance_records (company_id, vehicle_id, kind, description, cost_cents, vendor, performed_on)
		VALUES ($1,$2::uuid,$3,NULLIF($4,''),$5,NULLIF($6,''),$7::date)
	`, cid, vid, kind, desc, cost, vendor, when)
	http.Redirect(w, r, "/admin/maintenance?flash=Logged", http.StatusSeeOther)
}

// -- Reports --

func (h *Handler) Reports(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	h.render(w, r, "admin/reports.html", "Reports", "reports", map[string]any{"Reports": h.reportsData(r.Context(), cid)})
}
func (h *Handler) ReportsCSV(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rep := h.reportsData(r.Context(), cid)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
	fmt.Fprintln(w, "metric,value")
	for k, v := range rep {
		fmt.Fprintf(w, "%s,%d\n", k, v)
	}
}

// -- Settings --

func (h *Handler) Settings(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	type kv map[string]any
	general := kv{"businessName": "", "currency": "USD", "timezone": "UTC", "mileageUnit": "km", "phone": "", "address": ""}
	rental := kv{"minHours": 24, "maxDays": 90, "graceMinutes": 60, "depositDefaultCents": 20000}
	smtp := kv{"host": "", "port": "587", "user": "", "fromAddr": "", "fromName": "mosrental", "hasPassword": false}

	rows, _ := h.Pool.Query(r.Context(), `SELECT key, value FROM settings WHERE company_id=$1`, cid)
	if rows != nil {
		for rows.Next() {
			var k string
			var v []byte
			_ = rows.Scan(&k, &v)
			var m kv
			if err := jsonUnmarshal(v, &m); err != nil {
				continue
			}
			switch k {
			case "general":
				for kk, vv := range m { general[kk] = vv }
			case "rental":
				for kk, vv := range m { rental[kk] = vv }
			case "smtp":
				for kk, vv := range m {
					if kk == "pass" {
						if s, ok := vv.(string); ok && s != "" {
							smtp["hasPassword"] = true
						}
						continue
					}
					smtp[kk] = vv
				}
			}
		}
		rows.Close()
	}
	// If no DB smtp row, fall back to env values for display.
	if smtp["host"] == "" && h.EnvSMTP.Host != "" {
		smtp["host"] = h.EnvSMTP.Host
		smtp["port"] = h.EnvSMTP.Port
		smtp["user"] = h.EnvSMTP.User
		smtp["fromAddr"] = h.EnvSMTP.FromAddr
		smtp["fromName"] = h.EnvSMTP.FromName
		smtp["hasPassword"] = h.EnvSMTP.Pass != ""
		smtp["fromEnv"] = true
	}
	effective := h.resolveSMTPDirect(r.Context(), cid)
	tax := h.taxFor(r.Context(), cid)
	h.render(w, r, "admin/settings.html", "Settings", "settings", map[string]any{
		"General": general, "Rental": rental, "SMTP": smtp,
		"Tax":            tax,
		"SMTPConfigured": effective.Configured(),
		"Flash":          r.URL.Query().Get("flash"),
		"Err":            r.URL.Query().Get("err"),
	})
}

// Same shape as resolveSMTP but doesn't require the comms helper.
func (h *Handler) resolveSMTPDirect(ctx context.Context, cid uuid.UUID) mail.Config {
	cfg := h.EnvSMTP
	var raw []byte
	if err := h.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE company_id=$1 AND key='smtp'`, cid).Scan(&raw); err != nil {
		return cfg
	}
	var m map[string]string
	if err := jsonUnmarshal(raw, &m); err != nil {
		return cfg
	}
	if v := m["host"]; v != "" { cfg.Host = v }
	if v := m["port"]; v != "" { cfg.Port = v }
	if v := m["user"]; v != "" { cfg.User = v }
	if v := m["pass"]; v != "" { cfg.Pass = v }
	if v := m["fromAddr"]; v != "" { cfg.FromAddr = v }
	if v := m["fromName"]; v != "" { cfg.FromName = v }
	return cfg
}

func (h *Handler) SettingsPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	gen := map[string]any{
		"businessName": r.FormValue("businessName"),
		"currency":     r.FormValue("currency"),
		"timezone":     r.FormValue("timezone"),
		"phone":        r.FormValue("phone"),
		"address":      r.FormValue("address"),
		"mileageUnit":  "km",
	}
	n := func(k string) int { v, _ := strconv.Atoi(r.FormValue(k)); return v }
	rent := map[string]any{
		"minHours":            n("minHours"),
		"maxDays":             n("maxDays"),
		"graceMinutes":        n("graceMinutes"),
		"depositDefaultCents": n("depositDefaultCents"),
	}
	for key, val := range map[string]map[string]any{"general": gen, "rental": rent} {
		b, _ := jsonMarshal(val)
		_, _ = h.Pool.Exec(r.Context(), `
			INSERT INTO settings (company_id, key, value) VALUES ($1,$2,$3::jsonb)
			ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
		`, cid, key, string(b))
	}
	http.Redirect(w, r, "/admin/settings?flash=Saved", http.StatusSeeOther)
}

// SMTPPost saves SMTP settings. Blank password means "keep existing".
func (h *Handler) SMTPPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()

	// Load existing to preserve password if blank.
	existing := map[string]string{}
	var raw []byte
	if err := h.Pool.QueryRow(r.Context(), `SELECT value FROM settings WHERE company_id=$1 AND key='smtp'`, cid).Scan(&raw); err == nil {
		_ = jsonUnmarshal(raw, &existing)
	}

	host := strings.TrimSpace(r.FormValue("host"))
	port := strings.TrimSpace(r.FormValue("port"))
	user := strings.TrimSpace(r.FormValue("user"))
	pass := r.FormValue("pass")
	fromAddr := strings.TrimSpace(r.FormValue("fromAddr"))
	fromName := strings.TrimSpace(r.FormValue("fromName"))

	// Validation. Blank host/port/fromAddr effectively clears the config — allowed.
	if host != "" && port != "" && fromAddr != "" && !mail.ValidateAddress(fromAddr) {
		http.Redirect(w, r, "/admin/settings?err=Invalid+from+address#smtp", http.StatusSeeOther)
		return
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
			http.Redirect(w, r, "/admin/settings?err=Invalid+port#smtp", http.StatusSeeOther)
			return
		}
	}
	if pass == "" {
		pass = existing["pass"]
	}
	cfg := map[string]string{
		"host": host, "port": port, "user": user, "pass": pass,
		"fromAddr": fromAddr, "fromName": fromName,
	}
	b, _ := jsonMarshal(cfg)
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'smtp',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error()+"#smtp", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?flash=SMTP+saved#smtp", http.StatusSeeOther)
}

// AccountPost updates the signed-in staff user's email and/or password.
// Requires the current password.
func (h *Handler) AccountPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	_ = r.ParseForm()
	currentPw := r.FormValue("currentPassword")
	newEmail := strings.ToLower(strings.TrimSpace(r.FormValue("newEmail")))
	newPw := r.FormValue("newPassword")

	// Load the user's current hash.
	var hash, email, name string
	err := h.Pool.QueryRow(r.Context(), `SELECT password_hash, email, name FROM users WHERE id=$1`, s.UserID).Scan(&hash, &email, &name)
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err=User+not+found#account", http.StatusSeeOther)
		return
	}
	if !auth.VerifyPassword(hash, currentPw) {
		http.Redirect(w, r, "/admin/settings?err=Current+password+is+incorrect#account", http.StatusSeeOther)
		return
	}
	if newPw != "" && len(newPw) < 8 {
		http.Redirect(w, r, "/admin/settings?err=New+password+must+be+at+least+8+chars#account", http.StatusSeeOther)
		return
	}
	if newEmail != "" && !mail.ValidateAddress(newEmail) {
		http.Redirect(w, r, "/admin/settings?err=New+email+is+invalid#account", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	if newEmail != "" && newEmail != email {
		_, err := h.Pool.Exec(ctx, `UPDATE users SET email=$1, updated_at=NOW() WHERE id=$2`, newEmail, s.UserID)
		if err != nil {
			http.Redirect(w, r, "/admin/settings?err=Email+change+failed+(already+in+use?)#account", http.StatusSeeOther)
			return
		}
	}
	if newPw != "" {
		newHash, _ := auth.HashPassword(newPw)
		_, err := h.Pool.Exec(ctx, `UPDATE users SET password_hash=$1, updated_at=NOW() WHERE id=$2`, newHash, s.UserID)
		if err != nil {
			http.Redirect(w, r, "/admin/settings?err=Password+change+failed#account", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/admin/settings?flash=Account+updated#account", http.StatusSeeOther)
}

// SMTPTest sends a one-off test email using the saved config.
func (h *Handler) SMTPTest(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	to := strings.TrimSpace(r.FormValue("to"))
	if !mail.ValidateAddress(to) {
		http.Redirect(w, r, "/admin/settings?err=Invalid+recipient#smtp", http.StatusSeeOther)
		return
	}
	m := &mail.Sender{Cfg: h.resolveSMTPDirect(r.Context(), cid)}
	if !m.Cfg.Configured() {
		http.Redirect(w, r, "/admin/settings?err=Fill+in+host+port+from+address+first#smtp", http.StatusSeeOther)
		return
	}
	_, err := m.Send(to, "mosrental SMTP test", "Hi,\n\nThis is a test email from your mosrental Communication Center.\n\nIf you received this, your SMTP settings are working.\n\nSent by: "+s.Name)
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err=Send+failed:+"+err.Error()+"#smtp", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?flash=Test+email+sent+to+"+to+"#smtp", http.StatusSeeOther)
}
