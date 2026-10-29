package web

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// =============================================================================
// Fleet-prep: tokenised inspection workflow for fleet / prep staff.
//
// Flow:
//   1. On the admin reservation page, manager clicks "Fleet prep link".
//      ReservationFleetLink inserts a token (kind='both', purpose='fleet')
//      and returns a shareable URL to /fleet/<token>.
//   2. Fleet staff opens the link (no login required). FleetPrepGet renders
//      a page with two sections — "Pre-rental check" (before customer pickup)
//      and "Post-rental check" (after customer return). Each has odometer,
//      fuel, damage notes and multiple photo slots.
//   3. FleetPrepSave upserts the inspection row for that phase.
//   4. FleetPrepPhoto uploads a photo for a given slot and ties it to the
//      inspection row. "damage" slot can hold many; others replace.
//   5. Admin's reservation detail page renders inspection photos + damage
//      notes side by side.
// =============================================================================

type FleetInspection struct {
	ID            uuid.UUID
	Kind          string // 'pre' or 'post'
	OdometerKm    *int
	FuelPct       *int
	DamageNotes   *string
	CreatedAt     time.Time
	Photos        []InspectionPhoto
}

type InspectionPhoto struct {
	ID       uuid.UUID
	Slot     string
	FilePath string
	Caption  *string
}

// ReservationFleetLink generates a new fleet-prep token and redirects back
// with the sharable URL in a flash parameter.
func (h *Handler) ReservationFleetLink(w http.ResponseWriter, r *http.Request) {
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
	link := h.shareBase(r) + "/fleet/" + tok
	http.Redirect(w, r,
		"/admin/reservations/"+id+"?flash=Fleet+prep+link+ready&link="+link,
		http.StatusSeeOther)
}

// shareBase returns the base URL for a share link. Prefers the request's own
// host (so the generated link matches wherever the admin is browsing from)
// and falls back to the configured PublicBase when the request has no Host.
func (h *Handler) shareBase(r *http.Request) string {
	host := r.Host
	if host == "" {
		return h.PublicBase
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host
}

// =============================================================================
// Public fleet-prep workflow
// =============================================================================

type fleetPrepCtx struct {
	Token         string
	CompanyName   string
	BookingNumber string
	Status        string
	Vehicle       string
	VehiclePlate  string
	Customer      string
	PickupAt      time.Time
	ReturnAt      time.Time
	Pre           *FleetInspection
	Post          *FleetInspection
	ExpiresAt     time.Time
	Flash         string
	Err           string
}

// FleetPrepGet renders the full prep page. Loads/creates the two inspection
// rows lazily so the UI always has a stable row id to post photos to.
func (h *Handler) FleetPrepGet(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	ctx := r.Context()
	var cid, resID, vehID uuid.UUID
	var exp time.Time
	err := h.Pool.QueryRow(ctx, `
		SELECT pt.company_id, pt.reservation_id, r.vehicle_id, pt.expires_at
		FROM photo_upload_tokens pt
		JOIN reservations r ON r.id=pt.reservation_id
		WHERE pt.token=$1
	`, tok).Scan(&cid, &resID, &vehID, &exp)
	if err != nil || time.Now().After(exp) {
		h.render(w, r, "public/fleet_prep.html", "Link invalid", "", map[string]any{
			"Err": "This fleet-prep link is invalid or expired. Ask the manager to generate a new one.",
		})
		return
	}

	// Load context details.
	var ctxData fleetPrepCtx
	ctxData.Token = tok
	ctxData.ExpiresAt = exp
	var veh, plate, cust *string
	err = h.Pool.QueryRow(ctx, `
		SELECT co.name,
		       r.booking_number, r.status,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.license_plate,
		       COALESCE(cu.first_name||' '||cu.last_name,''),
		       r.pickup_at, r.return_at
		FROM reservations r
		JOIN companies co ON co.id=r.company_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE r.id=$1
	`, resID).Scan(&ctxData.CompanyName, &ctxData.BookingNumber, &ctxData.Status,
		&veh, &plate, &cust, &ctxData.PickupAt, &ctxData.ReturnAt)
	if err != nil {
		http.Error(w, "lookup failed", 500)
		return
	}
	if veh != nil {
		ctxData.Vehicle = strings.TrimSpace(*veh)
	}
	if plate != nil {
		ctxData.VehiclePlate = *plate
	}
	if cust != nil {
		ctxData.Customer = *cust
	}

	// Ensure pre + post inspection rows exist.
	preID, err := h.ensureInspectionReal(r, cid, resID, vehID, "pre")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	postID, err := h.ensureInspectionReal(r, cid, resID, vehID, "post")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ctxData.Pre = h.loadInspectionByID(r, preID)
	ctxData.Post = h.loadInspectionByID(r, postID)
	ctxData.Flash = r.URL.Query().Get("flash")
	ctxData.Err = r.URL.Query().Get("err")

	h.render(w, r, "public/fleet_prep.html", "Fleet prep · "+ctxData.BookingNumber, "", map[string]any{
		"C": ctxData,
	})
}

// loadInspectionByID loads one inspection row with its photos.
func (h *Handler) loadInspectionByID(r *http.Request, id uuid.UUID) *FleetInspection {
	ins := &FleetInspection{}
	err := h.Pool.QueryRow(r.Context(),
		`SELECT id, kind, odometer_km, fuel_level_pct, damage_notes, created_at
		   FROM inspections WHERE id=$1`, id,
	).Scan(&ins.ID, &ins.Kind, &ins.OdometerKm, &ins.FuelPct, &ins.DamageNotes, &ins.CreatedAt)
	if err != nil {
		return nil
	}
	ins.Photos = h.photosForInspection(r, ins.ID)
	return ins
}

// =============================================================================
// Save handlers
// =============================================================================

// FleetPrepSave updates odometer / fuel / damage notes for one inspection
// phase.
func (h *Handler) FleetPrepSave(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	kind := chi.URLParam(r, "kind")
	if kind != "pre" && kind != "post" {
		http.Error(w, "bad kind", 400)
		return
	}
	resID, cid, vehID, ok := h.resolveFleetToken(w, r, tok)
	if !ok {
		return
	}
	_ = r.ParseForm()
	odoStr := strings.TrimSpace(r.FormValue("odometerKm"))
	fuelStr := strings.TrimSpace(r.FormValue("fuelPct"))
	notes := strings.TrimSpace(r.FormValue("damageNotes"))
	var odo, fuel *int
	if n, err := strconv.Atoi(odoStr); err == nil && n > 0 {
		odo = &n
	}
	if n, err := strconv.Atoi(fuelStr); err == nil && n >= 0 && n <= 100 {
		fuel = &n
	}
	id, err := h.ensureInspectionReal(r, cid, resID, vehID, kind)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	_, err = h.Pool.Exec(r.Context(), `
		UPDATE inspections
		   SET odometer_km=$1, fuel_level_pct=$2, damage_notes=NULLIF($3,'')
		 WHERE id=$4
	`, odo, fuel, notes, id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/fleet/"+tok+"?flash="+kind+"+inspection+saved#"+kind, http.StatusSeeOther)
}

// FleetPrepPhoto accepts a multipart photo upload for one slot of one phase.
// For the "damage" slot we insert additional rows; for the fixed slots we
// replace any previous photo of the same slot (so re-shooting works).
func (h *Handler) FleetPrepPhoto(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	kind := chi.URLParam(r, "kind")
	if kind != "pre" && kind != "post" {
		http.Error(w, "bad kind", 400)
		return
	}
	resID, cid, vehID, ok := h.resolveFleetToken(w, r, tok)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "multipart parse", 400)
		return
	}
	slot := r.FormValue("slot")
	caption := strings.TrimSpace(r.FormValue("caption"))
	fixedSlots := map[string]bool{"front": true, "rear": true, "left": true, "right": true, "interior": true, "odometer": true, "damage": true}
	if !fixedSlots[slot] {
		http.Error(w, "unknown slot", 400)
		return
	}
	hdrs := r.MultipartForm.File["file"]
	if len(hdrs) == 0 {
		http.Redirect(w, r, "/fleet/"+tok+"?err=File+required#"+kind, http.StatusSeeOther)
		return
	}
	id, err := h.ensureInspectionReal(r, cid, resID, vehID, kind)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// If this is a fixed slot (not 'damage'), remove any previous photo for it.
	if slot != "damage" {
		_, _ = h.Pool.Exec(r.Context(), `DELETE FROM inspection_photos WHERE inspection_id=$1 AND slot=$2`, id, slot)
	}
	rel, err := h.Store.Save(hdrs[0], path.Join("inspections", id.String()))
	if err != nil {
		http.Error(w, "save failed: "+err.Error(), 400)
		return
	}
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO inspection_photos (company_id, inspection_id, slot, file_path, caption)
		VALUES ($1,$2,$3,$4,NULLIF($5,''))
	`, cid, id, slot, rel, caption)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Mark the inspection row as touched (keeps vehicle id synced in case it was blank).
	_, _ = h.Pool.Exec(r.Context(), `UPDATE inspections SET vehicle_id=$1 WHERE id=$2`, vehID, id)
	http.Redirect(w, r, "/fleet/"+tok+"?flash=Photo+saved#"+kind, http.StatusSeeOther)
}

// FleetPrepDeletePhoto removes a damage photo (fleet staff re-shoots).
func (h *Handler) FleetPrepDeletePhoto(w http.ResponseWriter, r *http.Request) {
	tok := chi.URLParam(r, "token")
	pid := chi.URLParam(r, "pid")
	// Make sure the photo belongs to this token's reservation.
	resID, _, _, ok := h.resolveFleetToken(w, r, tok)
	if !ok {
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `
		DELETE FROM inspection_photos
		WHERE id=$1
		  AND inspection_id IN (SELECT id FROM inspections WHERE reservation_id=$2)
	`, pid, resID)
	http.Redirect(w, r, "/fleet/"+tok+"?flash=Photo+removed", http.StatusSeeOther)
}

// =============================================================================
// Helpers
// =============================================================================

// resolveFleetToken validates the token and returns the reservation / company /
// vehicle ids. Writes an HTTP error and returns false if the token is bad.
func (h *Handler) resolveFleetToken(w http.ResponseWriter, r *http.Request, tok string) (resID, cid, vehID uuid.UUID, ok bool) {
	var exp time.Time
	err := h.Pool.QueryRow(r.Context(), `
		SELECT pt.company_id, pt.reservation_id, r.vehicle_id, pt.expires_at
		FROM photo_upload_tokens pt
		JOIN reservations r ON r.id=pt.reservation_id
		WHERE pt.token=$1
	`, tok).Scan(&cid, &resID, &vehID, &exp)
	if err != nil || time.Now().After(exp) {
		http.Error(w, "invalid or expired link", 410)
		return
	}
	ok = true
	return
}

// ensureInspectionReal returns the inspection row id for a reservation+kind,
// inserting one if missing.
func (h *Handler) ensureInspectionReal(r *http.Request, cid, resID, vehID uuid.UUID, kind string) (uuid.UUID, error) {
	var id uuid.UUID
	err := h.Pool.QueryRow(r.Context(),
		`SELECT id FROM inspections WHERE reservation_id=$1 AND kind=$2 LIMIT 1`,
		resID, kind,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	err = h.Pool.QueryRow(r.Context(), `
		INSERT INTO inspections (company_id, reservation_id, vehicle_id, kind)
		VALUES ($1,$2,$3,$4) RETURNING id
	`, cid, resID, vehID, kind).Scan(&id)
	return id, err
}

// =============================================================================
// Admin side — inspection report rendering helpers used by reservation detail.
// =============================================================================

// InspectionsForReservation returns both pre and post inspections (with
// photos) for admin display.
func (h *Handler) InspectionsForReservation(r *http.Request, resID uuid.UUID) (pre, post *FleetInspection) {
	rows, err := h.Pool.Query(r.Context(),
		`SELECT id, kind, odometer_km, fuel_level_pct, damage_notes, created_at
		   FROM inspections WHERE reservation_id=$1 ORDER BY created_at`, resID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	for rows.Next() {
		ins := &FleetInspection{}
		if err := rows.Scan(&ins.ID, &ins.Kind, &ins.OdometerKm, &ins.FuelPct, &ins.DamageNotes, &ins.CreatedAt); err == nil {
			ins.Photos = h.photosForInspection(r, ins.ID)
			if ins.Kind == "pre" {
				pre = ins
			} else if ins.Kind == "post" {
				post = ins
			}
		}
	}
	return
}

func (h *Handler) photosForInspection(r *http.Request, id uuid.UUID) []InspectionPhoto {
	out := []InspectionPhoto{}
	rows, err := h.Pool.Query(r.Context(),
		`SELECT id, slot, file_path, caption FROM inspection_photos
		   WHERE inspection_id=$1 ORDER BY created_at`, id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p InspectionPhoto
		if err := rows.Scan(&p.ID, &p.Slot, &p.FilePath, &p.Caption); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// =============================================================================
// Helpers for templates
// =============================================================================

// slotFor returns the first photo (if any) for a fixed slot. "damage" is
// multi-photo so it's handled separately in the template.
func (ins *FleetInspection) FirstPhoto(slot string) *InspectionPhoto {
	if ins == nil {
		return nil
	}
	for i := range ins.Photos {
		if ins.Photos[i].Slot == slot {
			return &ins.Photos[i]
		}
	}
	return nil
}

func (ins *FleetInspection) Damage() []InspectionPhoto {
	if ins == nil {
		return nil
	}
	out := []InspectionPhoto{}
	for _, p := range ins.Photos {
		if p.Slot == "damage" {
			out = append(out, p)
		}
	}
	return out
}

// Count of photos (across all slots) for the completeness indicator.
func (ins *FleetInspection) PhotoCount() int {
	if ins == nil {
		return 0
	}
	return len(ins.Photos)
}

// SlotLabels: canonical labels used by both the fleet-prep page and admin.
var slotLabels = []struct {
	Code, Label string
}{
	{"front", "Front"},
	{"rear", "Rear"},
	{"left", "Left side"},
	{"right", "Right side"},
	{"interior", "Interior"},
	{"odometer", "Odometer"},
}

func fleetSlotLabels() []struct{ Code, Label string } { return slotLabels }

// Shortening — unused warning suppressor for the dummy funcs above.
var _ = fmt.Sprintf
