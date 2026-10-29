package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type customerRow struct {
	ID            uuid.UUID `json:"id"`
	FirstName     string    `json:"firstName"`
	LastName      string    `json:"lastName"`
	Email         *string   `json:"email"`
	Phone         *string   `json:"phone"`
	Address       *string   `json:"address"`
	City          *string   `json:"city"`
	Country       *string   `json:"country"`
	LicenseNumber *string   `json:"licenseNumber"`
	LicenseExpiry *string   `json:"licenseExpiry"`
	Notes         *string   `json:"notes"`
	CreatedAt     string    `json:"createdAt"`
}
// (new fields street2/state/zip/birthday live on the DB; this JSON row stays stable for the legacy API.)

func (d *Deps) ListCustomers(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, first_name, last_name, email, phone, address, city, country,
		       license_number, license_expiry::text, notes, created_at::text
		FROM customers
		WHERE company_id = $1
		  AND ($2 = '' OR first_name ILIKE '%'||$2||'%' OR last_name ILIKE '%'||$2||'%'
		       OR email ILIKE '%'||$2||'%' OR phone ILIKE '%'||$2||'%')
		ORDER BY created_at DESC LIMIT 500
	`, c.CompanyID, q)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []customerRow{}
	for rows.Next() {
		var cr customerRow
		if err := rows.Scan(&cr.ID, &cr.FirstName, &cr.LastName, &cr.Email, &cr.Phone, &cr.Address,
			&cr.City, &cr.Country, &cr.LicenseNumber, &cr.LicenseExpiry, &cr.Notes, &cr.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		out = append(out, cr)
	}
	writeJSON(w, http.StatusOK, out)
}

type createCustomerReq struct {
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	Address       string `json:"address"`
	City          string `json:"city"`
	Country       string `json:"country"`
	LicenseNumber string `json:"licenseNumber"`
	LicenseExpiry string `json:"licenseExpiry"`
	Notes         string `json:"notes"`
}

func (d *Deps) CreateCustomer(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req createCustomerReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.FirstName) == "" || strings.TrimSpace(req.LastName) == "" {
		writeErr(w, http.StatusBadRequest, "firstName and lastName required")
		return
	}
	var id uuid.UUID
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO customers (company_id, first_name, last_name, email, phone, address, city, country, license_number, license_expiry, notes)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),NULLIF($10,'')::date,NULLIF($11,''))
		RETURNING id
	`, c.CompanyID, strings.TrimSpace(req.FirstName), strings.TrimSpace(req.LastName),
		strings.ToLower(strings.TrimSpace(req.Email)), req.Phone, req.Address, req.City, req.Country,
		req.LicenseNumber, req.LicenseExpiry, req.Notes).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (d *Deps) GetCustomer(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "id")
	var cr customerRow
	err := d.DB.Pool.QueryRow(r.Context(), `
		SELECT id, first_name, last_name, email, phone, address, city, country,
		       license_number, license_expiry::text, notes, created_at::text
		FROM customers WHERE company_id=$1 AND id=$2
	`, c.CompanyID, id).Scan(&cr.ID, &cr.FirstName, &cr.LastName, &cr.Email, &cr.Phone, &cr.Address,
		&cr.City, &cr.Country, &cr.LicenseNumber, &cr.LicenseExpiry, &cr.Notes, &cr.CreatedAt)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, cr)
}
