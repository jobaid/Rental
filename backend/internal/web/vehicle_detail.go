package web

import (
	"net/http"
	"path"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type VehiclePhoto struct {
	ID        uuid.UUID
	Kind      *string
	Caption   *string
	URL       string
	IsPrimary bool
	Order     int
	CreatedAt time.Time
}

type VehicleDetailRow struct {
	ID             uuid.UUID
	FleetNumber    *string
	LicensePlate   *string
	VIN            *string
	Make, Model    string
	Year           int
	Trim           *string
	Transmission   *string
	FuelType       *string
	Seats          *int
	ColorExterior  *string
	ColorInterior  *string
	OdometerKm     *int
	DailyRateCents int
	WeeklyRateCents *int
	MonthlyRateCents *int
	DepositCents   int
	Status         string
	Notes          *string
}

func (h *Handler) VehicleDetail(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	var v VehicleDetailRow
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, fleet_number, license_plate, vin, COALESCE(make,''), COALESCE(model,''), COALESCE(year,0),
		       trim, transmission, fuel_type, seats, color_exterior, color_interior, odometer_km,
		       daily_rate_cents, weekly_rate_cents, monthly_rate_cents, deposit_cents, status, notes
		FROM vehicles WHERE company_id=$1 AND id=$2
	`, cid, id).Scan(&v.ID, &v.FleetNumber, &v.LicensePlate, &v.VIN, &v.Make, &v.Model, &v.Year,
		&v.Trim, &v.Transmission, &v.FuelType, &v.Seats, &v.ColorExterior, &v.ColorInterior, &v.OdometerKm,
		&v.DailyRateCents, &v.WeeklyRateCents, &v.MonthlyRateCents, &v.DepositCents, &v.Status, &v.Notes)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	rows, _ := h.Pool.Query(r.Context(), `
		SELECT id, kind, caption, file_path, is_primary, display_order, created_at
		FROM vehicle_photos WHERE company_id=$1 AND vehicle_id=$2
		ORDER BY is_primary DESC, display_order ASC, created_at ASC
	`, cid, id)
	photos := []VehiclePhoto{}
	if rows != nil {
		for rows.Next() {
			var p VehiclePhoto
			var fp string
			if err := rows.Scan(&p.ID, &p.Kind, &p.Caption, &fp, &p.IsPrimary, &p.Order, &p.CreatedAt); err == nil {
				p.URL = "/uploads/" + fp
				photos = append(photos, p)
			}
		}
		rows.Close()
	}

	periods, _ := h.ratePeriodsFor(r.Context(), cid, v.ID)
	tax := h.taxFor(r.Context(), cid)

	// Features — load the vehicle's stored map.
	var featRaw []byte
	_ = h.Pool.QueryRow(r.Context(), `SELECT features FROM vehicles WHERE id=$1`, v.ID).Scan(&featRaw)
	featSet := map[string]bool{}
	if len(featRaw) > 0 {
		_ = jsonUnmarshal(featRaw, &featSet)
	}

	// Addons: full catalog + which are enabled for this vehicle.
	type AddonLite struct {
		Code, Name string
		PriceCents int
		PricePer   string
	}
	addons := []AddonLite{}
	rowsA, _ := h.Pool.Query(r.Context(),
		`SELECT code, name, price_cents, price_per FROM addon_catalog WHERE company_id=$1 AND is_active ORDER BY display_order, name`, cid)
	if rowsA != nil {
		for rowsA.Next() {
			var a AddonLite
			_ = rowsA.Scan(&a.Code, &a.Name, &a.PriceCents, &a.PricePer)
			addons = append(addons, a)
		}
		rowsA.Close()
	}
	enabled := map[string]bool{}
	rowsE, _ := h.Pool.Query(r.Context(),
		`SELECT addon_code FROM vehicle_addons WHERE vehicle_id=$1`, v.ID)
	if rowsE != nil {
		for rowsE.Next() {
			var c string
			if err := rowsE.Scan(&c); err == nil {
				enabled[c] = true
			}
		}
		rowsE.Close()
	}

	h.render(w, r, "admin/vehicle_detail.html", "Vehicle · "+v.Make+" "+v.Model, "vehicles", map[string]any{
		"V":            v,
		"Photos":       photos,
		"Periods":      periods,
		"Tax":          tax,
		"FeatureList":  featureOpts,
		"FeatureSet":   featSet,
		"Addons":       addons,
		"EnabledAddons": enabled,
		"Flash":        r.URL.Query().Get("flash"),
		"Err":          r.URL.Query().Get("err"),
	})
}

func (h *Handler) VehiclePhotoUpload(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")

	// Verify vehicle belongs to this company.
	var ok bool
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM vehicles WHERE id=$1 AND company_id=$2)`, vid, cid).Scan(&ok)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// 20 MB max per upload (storage.Save also caps at 15 MB per file).
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err=Upload+too+large", http.StatusSeeOther)
		return
	}
	kind := r.FormValue("kind")
	caption := r.FormValue("caption")
	primary := r.FormValue("primary") == "on"

	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err=Pick+a+file+first", http.StatusSeeOther)
		return
	}
	rel, err := h.Store.Save(headers[0], path.Join("vehicles", vid))
	if err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err="+err.Error(), http.StatusSeeOther)
		return
	}
	if primary {
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE vehicle_photos SET is_primary=FALSE WHERE company_id=$1 AND vehicle_id=$2`, cid, vid)
	}
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO vehicle_photos (company_id, vehicle_id, kind, caption, file_path, is_primary, uploaded_by)
		VALUES ($1,$2::uuid,NULLIF($3,''),NULLIF($4,''),$5,$6,$7)
	`, cid, vid, kind, caption, rel, primary, s.UserID)
	if err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err=Save+failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Photo+uploaded", http.StatusSeeOther)
}

func (h *Handler) VehiclePhotoDelete(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")
	pid := chi.URLParam(r, "pid")
	_, _ = h.Pool.Exec(r.Context(),
		`DELETE FROM vehicle_photos WHERE company_id=$1 AND vehicle_id=$2 AND id=$3`, cid, vid, pid)
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Photo+deleted", http.StatusSeeOther)
}

// VehicleFeaturesPost saves the features map + the vehicle's enabled addon list.
func (h *Handler) VehicleFeaturesPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")
	_ = r.ParseForm()
	features := map[string]bool{}
	for _, v := range r.Form["feature"] {
		features[v] = true
	}
	featJSON, _ := jsonMarshal(features)

	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err="+err.Error(), http.StatusSeeOther)
		return
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE vehicles SET features=$1::jsonb WHERE company_id=$2 AND id=$3`, string(featJSON), cid, vid)
	if err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err="+err.Error(), http.StatusSeeOther)
		return
	}
	_, _ = tx.Exec(ctx, `DELETE FROM vehicle_addons WHERE vehicle_id=$1`, vid)
	for _, code := range r.Form["addon"] {
		_, _ = tx.Exec(ctx, `INSERT INTO vehicle_addons (company_id, vehicle_id, addon_code) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, cid, vid, code)
	}
	_ = tx.Commit(ctx)
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Features+%26+add-ons+saved#features", http.StatusSeeOther)
}

func jsonMarshalMust(v any) string { b, _ := jsonMarshal(v); return string(b) }

func (h *Handler) VehiclePhotoMakePrimary(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")
	pid := chi.URLParam(r, "pid")
	tx, err := h.Pool.Begin(r.Context())
	if err != nil {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err=db", http.StatusSeeOther)
		return
	}
	defer tx.Rollback(r.Context())
	_, _ = tx.Exec(r.Context(), `UPDATE vehicle_photos SET is_primary=FALSE WHERE company_id=$1 AND vehicle_id=$2`, cid, vid)
	_, _ = tx.Exec(r.Context(), `UPDATE vehicle_photos SET is_primary=TRUE WHERE company_id=$1 AND vehicle_id=$2 AND id=$3`, cid, vid, pid)
	_ = tx.Commit(r.Context())
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Primary+photo+updated", http.StatusSeeOther)
}
