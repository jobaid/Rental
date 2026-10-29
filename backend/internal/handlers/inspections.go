package handlers

import (
	"encoding/json"
	"net/http"
	"path"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type startInspReq struct {
	ReservationID string         `json:"reservationId"`
	VehicleID     string         `json:"vehicleId"`
	Kind          string         `json:"kind"` // pre | post
	FuelLevelPct  int            `json:"fuelLevelPct"`
	OdometerKm    int            `json:"odometerKm"`
	DamageNotes   string         `json:"damageNotes"`
	Checklist     map[string]any `json:"checklist"`
}

// CreateInspection records a pre- or post-rental inspection.
// It also writes a mileage_record for traceability and (for pre) sets vehicle status to rented
// if kind=pre on an active reservation; for post, moves vehicle to inspection_pending.
func (d *Deps) CreateInspection(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req startInspReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.VehicleID == "" || (req.Kind != "pre" && req.Kind != "post") {
		writeErr(w, http.StatusBadRequest, "vehicleId and kind (pre|post) required")
		return
	}
	cl, _ := json.Marshal(req.Checklist)
	if string(cl) == "null" || len(cl) == 0 {
		cl = []byte(`{}`)
	}

	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	var id uuid.UUID
	err = tx.QueryRow(r.Context(), `
		INSERT INTO inspections (company_id, reservation_id, vehicle_id, kind, fuel_level_pct, odometer_km, damage_notes, checklist, inspected_by)
		VALUES ($1, NULLIF($2,'')::uuid, $3::uuid, $4, NULLIF($5,0), NULLIF($6,0), NULLIF($7,''), $8::jsonb, $9)
		RETURNING id
	`, c.CompanyID, req.ReservationID, req.VehicleID, req.Kind, req.FuelLevelPct, req.OdometerKm, req.DamageNotes, string(cl), c.UserID).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}

	if req.OdometerKm > 0 {
		mKind := "out"
		if req.Kind == "post" {
			mKind = "in"
		}
		_, _ = tx.Exec(r.Context(), `
			INSERT INTO mileage_records (company_id, vehicle_id, reservation_id, kind, odometer_km, fuel_level_pct, recorded_by)
			VALUES ($1,$2::uuid,NULLIF($3,'')::uuid,$4,$5,NULLIF($6,0),$7)
		`, c.CompanyID, req.VehicleID, req.ReservationID, mKind, req.OdometerKm, req.FuelLevelPct, c.UserID)
		_, _ = tx.Exec(r.Context(), `UPDATE vehicles SET odometer_km=$1 WHERE company_id=$2 AND id=$3`,
			req.OdometerKm, c.CompanyID, req.VehicleID)
	}

	// Vehicle status side-effects.
	if req.Kind == "pre" {
		_, _ = tx.Exec(r.Context(), `UPDATE vehicles SET status='rented' WHERE company_id=$1 AND id=$2`, c.CompanyID, req.VehicleID)
		if req.ReservationID != "" {
			_, _ = tx.Exec(r.Context(), `UPDATE reservations SET status='checked_out' WHERE company_id=$1 AND id=$2`, c.CompanyID, req.ReservationID)
		}
	} else {
		_, _ = tx.Exec(r.Context(), `UPDATE vehicles SET status='inspection_pending' WHERE company_id=$1 AND id=$2`, c.CompanyID, req.VehicleID)
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// UploadInspectionPhoto attaches a photo to an inspection record.
func (d *Deps) UploadInspectionPhoto(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	iid := chi.URLParam(r, "id")
	var ok bool
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM inspections WHERE id=$1 AND company_id=$2)`,
		iid, c.CompanyID).Scan(&ok)
	if !ok {
		writeErr(w, http.StatusNotFound, "inspection not found")
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart parse")
		return
	}
	kind := r.FormValue("kind")
	caption := r.FormValue("caption")
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	rel, err := d.Store.Save(headers[0], path.Join("inspections", iid))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "save failed: "+err.Error())
		return
	}
	var id uuid.UUID
	err = d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO inspection_photos (inspection_id, kind, caption, file_path)
		VALUES ($1::uuid, NULLIF($2,''), NULLIF($3,''), $4) RETURNING id
	`, iid, kind, caption, rel).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "url": "/uploads/" + rel})
}

// ListInspections for a reservation (pre + post with photos) — enables the side-by-side compare UI.
func (d *Deps) ListInspections(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rid := r.URL.Query().Get("reservationId")
	vid := r.URL.Query().Get("vehicleId")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, reservation_id, vehicle_id, kind, fuel_level_pct, odometer_km, damage_notes, checklist, created_at
		FROM inspections WHERE company_id=$1
		  AND ($2='' OR reservation_id=$2::uuid)
		  AND ($3='' OR vehicle_id=$3::uuid)
		ORDER BY created_at DESC
	`, c.CompanyID, rid, vid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	defer rows.Close()
	type insp struct {
		ID          uuid.UUID       `json:"id"`
		ReservationID *uuid.UUID    `json:"reservationId"`
		VehicleID   uuid.UUID       `json:"vehicleId"`
		Kind        string          `json:"kind"`
		FuelLevelPct *int           `json:"fuelLevelPct"`
		OdometerKm  *int            `json:"odometerKm"`
		DamageNotes *string         `json:"damageNotes"`
		Checklist   json.RawMessage `json:"checklist"`
		Photos      []photoRow      `json:"photos"`
		CreatedAt   time.Time       `json:"createdAt"`
	}
	out := []insp{}
	ids := []uuid.UUID{}
	for rows.Next() {
		var i insp
		if err := rows.Scan(&i.ID, &i.ReservationID, &i.VehicleID, &i.Kind, &i.FuelLevelPct, &i.OdometerKm, &i.DamageNotes, &i.Checklist, &i.CreatedAt); err == nil {
			out = append(out, i)
			ids = append(ids, i.ID)
		}
	}
	// Fetch photos per inspection.
	for idx := range out {
		ps, _ := d.DB.Pool.Query(r.Context(), `
			SELECT id, kind, caption, file_path, created_at
			FROM inspection_photos WHERE inspection_id=$1 ORDER BY created_at ASC
		`, out[idx].ID)
		if ps != nil {
			for ps.Next() {
				var p photoRow
				var fp string
				if err := ps.Scan(&p.ID, &p.Kind, &p.Caption, &fp, &p.CreatedAt); err == nil {
					p.URL = "/uploads/" + fp
					out[idx].Photos = append(out[idx].Photos, p)
				}
			}
			ps.Close()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type photoRow struct {
	ID        uuid.UUID `json:"id"`
	Kind      *string   `json:"kind"`
	Caption   *string   `json:"caption"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}
