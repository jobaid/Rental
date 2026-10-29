package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

type oilReq struct {
	VehicleID    string `json:"vehicleId"`
	PerformedOn  string `json:"performedOn"`
	OdometerKm   int    `json:"odometerKm"`
	OilType      string `json:"oilType"`
	OilViscosity string `json:"oilViscosity"`
	OilFilter    string `json:"oilFilter"`
	NextDueDate  string `json:"nextDueDate"`
	NextDueKm    int    `json:"nextDueKm"`
	Vendor       string `json:"vendor"`
	CostCents    int    `json:"costCents"`
	Notes        string `json:"notes"`
}

func (d *Deps) ListOilChanges(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT o.id, o.vehicle_id,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       o.performed_on::text, o.odometer_km, o.oil_type, o.oil_viscosity, o.oil_filter,
		       o.next_due_date::text, o.next_due_km, o.vendor, o.cost_cents, o.notes
		FROM oil_changes o JOIN vehicles v ON v.id=o.vehicle_id
		WHERE o.company_id=$1 ORDER BY o.performed_on DESC LIMIT 500
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type row struct {
		ID          uuid.UUID `json:"id"`
		VehicleID   uuid.UUID `json:"vehicleId"`
		Vehicle     string    `json:"vehicle"`
		PerformedOn string    `json:"performedOn"`
		OdometerKm  *int      `json:"odometerKm"`
		OilType     *string   `json:"oilType"`
		OilVisc     *string   `json:"oilViscosity"`
		OilFilter   *string   `json:"oilFilter"`
		NextDueDate *string   `json:"nextDueDate"`
		NextDueKm   *int      `json:"nextDueKm"`
		Vendor      *string   `json:"vendor"`
		CostCents   int       `json:"costCents"`
		Notes       *string   `json:"notes"`
	}
	out := []row{}
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.ID, &rr.VehicleID, &rr.Vehicle, &rr.PerformedOn, &rr.OdometerKm,
			&rr.OilType, &rr.OilVisc, &rr.OilFilter, &rr.NextDueDate, &rr.NextDueKm,
			&rr.Vendor, &rr.CostCents, &rr.Notes); err == nil {
			out = append(out, rr)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) CreateOilChange(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req oilReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.VehicleID == "" {
		writeErr(w, http.StatusBadRequest, "vehicleId required")
		return
	}
	if req.PerformedOn == "" {
		req.PerformedOn = time.Now().Format("2006-01-02")
	}
	tx, err := d.DB.Pool.Begin(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(r.Context())

	var id uuid.UUID
	err = tx.QueryRow(r.Context(), `
		INSERT INTO oil_changes (company_id, vehicle_id, performed_on, odometer_km, oil_type, oil_viscosity, oil_filter,
		  next_due_date, next_due_km, vendor, cost_cents, notes)
		VALUES ($1,$2::uuid,$3::date,NULLIF($4,0),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),
		  NULLIF($8,'')::date, NULLIF($9,0), NULLIF($10,''), $11, NULLIF($12,''))
		RETURNING id
	`, c.CompanyID, req.VehicleID, req.PerformedOn, req.OdometerKm, req.OilType, req.OilViscosity, req.OilFilter,
		req.NextDueDate, req.NextDueKm, req.Vendor, req.CostCents, req.Notes).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	// Keep vehicle master in sync.
	_, _ = tx.Exec(r.Context(), `
		UPDATE vehicles
		   SET oil_type=COALESCE(NULLIF($1,''), oil_type),
		       oil_viscosity=COALESCE(NULLIF($2,''), oil_viscosity),
		       oil_filter=COALESCE(NULLIF($3,''), oil_filter),
		       next_oil_change_date=COALESCE(NULLIF($4,'')::date, next_oil_change_date),
		       next_oil_change_km=COALESCE(NULLIF($5,0), next_oil_change_km)
		 WHERE company_id=$6 AND id=$7
	`, req.OilType, req.OilViscosity, req.OilFilter, req.NextDueDate, req.NextDueKm, c.CompanyID, req.VehicleID)

	if err := tx.Commit(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// Alerts: expiring/overdue oil, insurance, registration, inspection.
func (d *Deps) FleetAlerts(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id,
		       COALESCE(year::text,'')||' '||COALESCE(make,'')||' '||COALESCE(model,'') AS label,
		       next_oil_change_date, next_oil_change_km, odometer_km,
		       registration_expiry, insurance_expiry, inspection_expiry
		FROM vehicles WHERE company_id=$1
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type alert struct {
		VehicleID uuid.UUID `json:"vehicleId"`
		Label     string    `json:"vehicle"`
		Kind      string    `json:"kind"`
		Message   string    `json:"message"`
	}
	out := []alert{}
	today := time.Now()
	soon := today.Add(14 * 24 * time.Hour)
	for rows.Next() {
		var id uuid.UUID
		var label string
		var oilDate *time.Time
		var oilKm, odo *int
		var reg, ins, insp *time.Time
		if err := rows.Scan(&id, &label, &oilDate, &oilKm, &odo, &reg, &ins, &insp); err != nil {
			continue
		}
		add := func(kind, msg string) { out = append(out, alert{id, label, kind, msg}) }
		if oilDate != nil && !oilDate.After(soon) {
			if oilDate.Before(today) {
				add("oil_overdue", "Oil change overdue since "+oilDate.Format("2006-01-02"))
			} else {
				add("oil_due", "Oil change due by "+oilDate.Format("2006-01-02"))
			}
		}
		if oilKm != nil && odo != nil && *odo >= *oilKm {
			add("oil_overdue_km", "Odometer passed oil-change threshold")
		}
		for name, exp := range map[string]*time.Time{"registration_expiry": reg, "insurance_expiry": ins, "inspection_expiry": insp} {
			if exp == nil {
				continue
			}
			if exp.Before(today) {
				add(name+"_expired", name+" expired on "+exp.Format("2006-01-02"))
			} else if exp.Before(soon) {
				add(name+"_due", name+" expires on "+exp.Format("2006-01-02"))
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Availability dashboard: every vehicle + latest state in one call.
func (d *Deps) AvailabilityDashboard(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT v.id, v.license_plate, COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.status, v.odometer_km, b.name,
		       (SELECT booking_number FROM reservations r
		          WHERE r.vehicle_id=v.id AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		          ORDER BY r.pickup_at ASC LIMIT 1),
		       (SELECT r.pickup_at FROM reservations r
		          WHERE r.vehicle_id=v.id AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		          ORDER BY r.pickup_at ASC LIMIT 1),
		       (SELECT r.return_at FROM reservations r
		          WHERE r.vehicle_id=v.id AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		          ORDER BY r.pickup_at ASC LIMIT 1)
		FROM vehicles v LEFT JOIN branches b ON b.id=v.branch_id
		WHERE v.company_id=$1 ORDER BY v.status, v.license_plate NULLS LAST
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		VehicleID   uuid.UUID `json:"vehicleId"`
		Plate       *string   `json:"licensePlate"`
		Label       string    `json:"vehicle"`
		Status      string    `json:"status"`
		OdometerKm  *int      `json:"odometerKm"`
		Branch      *string   `json:"branch"`
		BookingNum  *string   `json:"bookingNumber"`
		PickupAt    *time.Time `json:"pickupAt"`
		ReturnAt    *time.Time `json:"returnAt"`
	}
	out := []row{}
	for rows.Next() {
		var rr row
		if err := rows.Scan(&rr.VehicleID, &rr.Plate, &rr.Label, &rr.Status, &rr.OdometerKm, &rr.Branch,
			&rr.BookingNum, &rr.PickupAt, &rr.ReturnAt); err == nil {
			out = append(out, rr)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
