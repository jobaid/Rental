package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

type blockReq struct {
	VehicleID string    `json:"vehicleId"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Reason    string    `json:"reason"`
}

func (d *Deps) ListBlocks(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT b.id, b.vehicle_id, COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       b.starts_at, b.ends_at, b.reason
		FROM blocked_periods b JOIN vehicles v ON v.id=b.vehicle_id
		WHERE b.company_id=$1 ORDER BY b.starts_at DESC LIMIT 500
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID       uuid.UUID `json:"id"`
		VehicleID uuid.UUID `json:"vehicleId"`
		Vehicle  string    `json:"vehicle"`
		StartsAt time.Time `json:"startsAt"`
		EndsAt   time.Time `json:"endsAt"`
		Reason   *string   `json:"reason"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.VehicleID, &r.Vehicle, &r.StartsAt, &r.EndsAt, &r.Reason); err == nil {
			out = append(out, r)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) CreateBlock(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req blockReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.VehicleID == "" || req.StartsAt.IsZero() || req.EndsAt.IsZero() || !req.EndsAt.After(req.StartsAt) {
		writeErr(w, http.StatusBadRequest, "vehicleId, startsAt, endsAt (end > start) required")
		return
	}
	var id string
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO blocked_periods (company_id, vehicle_id, starts_at, ends_at, reason)
		VALUES ($1,$2::uuid,$3,$4,NULLIF($5,'')) RETURNING id
	`, c.CompanyID, req.VehicleID, req.StartsAt, req.EndsAt, req.Reason).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

type maintReq struct {
	VehicleID   string `json:"vehicleId"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	CostCents   int    `json:"costCents"`
	OdometerKm  int    `json:"odometerKm"`
	Vendor      string `json:"vendor"`
	PerformedOn string `json:"performedOn"`
}

func (d *Deps) ListMaintenance(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT m.id, m.vehicle_id,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       m.kind, m.description, m.cost_cents, m.odometer_km, m.vendor, m.performed_on::text
		FROM maintenance_records m JOIN vehicles v ON v.id=m.vehicle_id
		WHERE m.company_id=$1 ORDER BY m.performed_on DESC LIMIT 500
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID         uuid.UUID `json:"id"`
		VehicleID  uuid.UUID `json:"vehicleId"`
		Vehicle    string    `json:"vehicle"`
		Kind       string    `json:"kind"`
		Description *string  `json:"description"`
		CostCents  int       `json:"costCents"`
		OdometerKm *int      `json:"odometerKm"`
		Vendor     *string   `json:"vendor"`
		PerformedOn string   `json:"performedOn"`
	}
	out := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.VehicleID, &r.Vehicle, &r.Kind, &r.Description, &r.CostCents, &r.OdometerKm, &r.Vendor, &r.PerformedOn); err == nil {
			out = append(out, r)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) CreateMaintenance(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req maintReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.VehicleID == "" {
		writeErr(w, http.StatusBadRequest, "vehicleId required")
		return
	}
	if req.Kind == "" {
		req.Kind = "service"
	}
	if req.PerformedOn == "" {
		req.PerformedOn = time.Now().Format("2006-01-02")
	}
	var id string
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO maintenance_records (company_id, vehicle_id, kind, description, cost_cents, odometer_km, vendor, performed_on)
		VALUES ($1,$2::uuid,$3,NULLIF($4,''),$5,NULLIF($6,0),NULLIF($7,''),$8::date)
		RETURNING id
	`, c.CompanyID, req.VehicleID, req.Kind, req.Description, req.CostCents, req.OdometerKm, req.Vendor, req.PerformedOn).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// Reports: aggregate numbers from real data.
func (d *Deps) Reports(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	out := map[string]any{}
	var revenue int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE company_id=$1 AND status='succeeded'`,
		c.CompanyID).Scan(&revenue)
	out["revenueCents"] = revenue

	var outstanding int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COALESCE(SUM(GREATEST(total_cents-paid_cents,0)),0) FROM reservations
		 WHERE company_id=$1 AND status IN ('pending','confirmed','checked_out')`,
		c.CompanyID).Scan(&outstanding)
	out["outstandingCents"] = outstanding

	var bookings int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1`, c.CompanyID).Scan(&bookings)
	out["bookings"] = bookings

	var cancelled int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='cancelled'`,
		c.CompanyID).Scan(&cancelled)
	out["cancelled"] = cancelled

	var fleet, available int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, c.CompanyID).Scan(&fleet)
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='available'`, c.CompanyID).Scan(&available)
	out["fleet"] = fleet
	if fleet > 0 {
		out["utilizationPct"] = int(100 * (fleet - available) / fleet)
	} else {
		out["utilizationPct"] = 0
	}

	var maint int
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT COALESCE(SUM(cost_cents),0) FROM maintenance_records WHERE company_id=$1`,
		c.CompanyID).Scan(&maint)
	out["maintenanceCents"] = maint
	writeJSON(w, http.StatusOK, out)
}
