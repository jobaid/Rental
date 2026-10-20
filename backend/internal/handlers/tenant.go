package handlers

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Tenant-scoped handlers. Every query includes company_id from the JWT claims,
// which is the enforcement line for tenant isolation.

// ListBranches returns branches for the caller's company.
func (d *Deps) ListBranches(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, name, address, city, country, timezone, is_active
		FROM branches WHERE company_id = $1 ORDER BY name
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type branch struct {
		ID       uuid.UUID `json:"id"`
		Name     string    `json:"name"`
		Address  *string   `json:"address"`
		City     *string   `json:"city"`
		Country  *string   `json:"country"`
		Timezone string    `json:"timezone"`
		IsActive bool      `json:"isActive"`
	}
	out := []branch{}
	for rows.Next() {
		var b branch
		if err := rows.Scan(&b.ID, &b.Name, &b.Address, &b.City, &b.Country, &b.Timezone, &b.IsActive); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		out = append(out, b)
	}
	writeJSON(w, http.StatusOK, out)
}

type createBranchReq struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Country  string `json:"country"`
	Timezone string `json:"timezone"`
}

func (d *Deps) CreateBranch(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createBranchReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	var id uuid.UUID
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO branches (company_id, name, address, city, country, timezone)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6)
		RETURNING id
	`, c.CompanyID, req.Name, req.Address, req.City, req.Country, req.Timezone).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// ListVehicles returns the fleet for the caller's company.
func (d *Deps) ListVehicles(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, fleet_number, license_plate, make, model, year, status,
		       daily_rate_cents, deposit_cents
		FROM vehicles WHERE company_id = $1 ORDER BY created_at DESC LIMIT 500
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type v struct {
		ID              uuid.UUID `json:"id"`
		FleetNumber     *string   `json:"fleetNumber"`
		LicensePlate    *string   `json:"licensePlate"`
		Make            *string   `json:"make"`
		Model           *string   `json:"model"`
		Year            *int      `json:"year"`
		Status          string    `json:"status"`
		DailyRateCents  int       `json:"dailyRateCents"`
		DepositCents    int       `json:"depositCents"`
	}
	out := []v{}
	for rows.Next() {
		var row v
		if err := rows.Scan(&row.ID, &row.FleetNumber, &row.LicensePlate, &row.Make, &row.Model, &row.Year, &row.Status, &row.DailyRateCents, &row.DepositCents); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

type createVehicleReq struct {
	FleetNumber    string `json:"fleetNumber"`
	LicensePlate   string `json:"licensePlate"`
	Make           string `json:"make"`
	Model          string `json:"model"`
	Year           int    `json:"year"`
	DailyRateCents int    `json:"dailyRateCents"`
	DepositCents   int    `json:"depositCents"`
}

func (d *Deps) CreateVehicle(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createVehicleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Make == "" || req.Model == "" {
		writeErr(w, http.StatusBadRequest, "make and model required")
		return
	}
	if req.DailyRateCents < 0 || req.DepositCents < 0 {
		writeErr(w, http.StatusBadRequest, "rates must be non-negative")
		return
	}
	var id uuid.UUID
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO vehicles (company_id, fleet_number, license_plate, make, model, year, daily_rate_cents, deposit_cents, status)
		VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4, $5, NULLIF($6,0), $7, $8, 'available')
		RETURNING id
	`, c.CompanyID, req.FleetNumber, req.LicensePlate, req.Make, req.Model, req.Year, req.DailyRateCents, req.DepositCents).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed (duplicate fleet number or plate?)")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// ManagerStats: KPI cards for the manager dashboard, strictly tenant-scoped.
func (d *Deps) ManagerStats(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	stats := map[string]int{}
	queries := map[string]string{
		"fleetSize":   `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`,
		"available":   `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='available'`,
		"rented":      `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='rented'`,
		"maintenance": `SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='maintenance'`,
		"branches":    `SELECT COUNT(*) FROM branches WHERE company_id=$1`,
		"customers":   `SELECT COUNT(*) FROM customers WHERE company_id=$1`,
	}
	for k, q := range queries {
		var n int
		if err := d.DB.Pool.QueryRow(r.Context(), q, c.CompanyID).Scan(&n); err != nil {
			writeErr(w, http.StatusInternalServerError, "db error")
			return
		}
		stats[k] = n
	}
	writeJSON(w, http.StatusOK, stats)
}
