package handlers

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/rentalsoftware/backend/internal/auth"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

type companyCreateReq struct {
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	PlanCode      string `json:"planCode"`
	BillingCycle  string `json:"billingCycle"`
	OwnerName     string `json:"ownerName"`
	OwnerEmail    string `json:"ownerEmail"`
	OwnerPassword string `json:"ownerPassword"`
}

type companyRow struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	Status        string    `json:"status"`
	PlanCode      *string   `json:"planCode,omitempty"`
	BillingCycle  *string   `json:"billingCycle,omitempty"`
	CreatedAt     string    `json:"createdAt"`
}

// CreateCompany: platform owner provisions a new tenant plus its first company_owner user.
func (d *Deps) CreateCompany(w http.ResponseWriter, r *http.Request) {
	var req companyCreateReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	req.OwnerEmail = strings.ToLower(strings.TrimSpace(req.OwnerEmail))
	if req.Name == "" || !slugRe.MatchString(req.Slug) {
		writeErr(w, http.StatusBadRequest, "name and valid slug required (3-40 chars, lowercase)")
		return
	}
	if req.OwnerEmail == "" || len(req.OwnerPassword) < 8 || req.OwnerName == "" {
		writeErr(w, http.StatusBadRequest, "owner name, email and password (>=8 chars) required")
		return
	}
	if req.BillingCycle != "" && req.BillingCycle != "monthly" && req.BillingCycle != "annual" {
		writeErr(w, http.StatusBadRequest, "billingCycle must be monthly or annual")
		return
	}

	ctx := r.Context()
	tx, err := d.DB.Pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var planID *uuid.UUID
	if req.PlanCode != "" {
		var pid uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM subscription_plans WHERE code=$1`, req.PlanCode).Scan(&pid)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "unknown planCode")
			return
		}
		planID = &pid
	}

	var cid uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO companies (name, slug, plan_id, billing_cycle, status)
		VALUES ($1, $2, $3, NULLIF($4,''), 'active')
		RETURNING id
	`, req.Name, req.Slug, planID, req.BillingCycle).Scan(&cid)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "slug already in use or invalid")
		return
	}

	hash, err := auth.HashPassword(req.OwnerPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash error")
		return
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO users (company_id, email, password_hash, name, role, is_active)
		VALUES ($1, $2, $3, $4, 'company_owner', TRUE)
	`, cid, req.OwnerEmail, hash, req.OwnerName)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "owner email already used in this company")
		return
	}

	actor := claimsFromReq(r)
	_, _ = tx.Exec(ctx, `
		INSERT INTO audit_events (company_id, actor_user_id, action, target_type, target_id, metadata)
		VALUES ($1, $2, 'company.create', 'company', $3, '{}')
	`, cid, actor.UserID, cid.String())

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": cid, "slug": req.Slug})
}

// ListCompanies: platform-owner view of all tenants.
func (d *Deps) ListCompanies(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT c.id, c.name, c.slug, c.status, p.code, c.billing_cycle, c.created_at::text
		FROM companies c LEFT JOIN subscription_plans p ON p.id = c.plan_id
		ORDER BY c.created_at DESC LIMIT 500
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []companyRow{}
	for rows.Next() {
		var row companyRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Slug, &row.Status, &row.PlanCode, &row.BillingCycle, &row.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// OwnerStats: counts for the owner dashboard.
func (d *Deps) OwnerStats(w http.ResponseWriter, r *http.Request) {
	stats := map[string]int{}
	queries := map[string]string{
		"companies":          `SELECT COUNT(*) FROM companies`,
		"companiesActive":    `SELECT COUNT(*) FROM companies WHERE status='active'`,
		"companiesSuspended": `SELECT COUNT(*) FROM companies WHERE status='suspended'`,
		"vehicles":           `SELECT COUNT(*) FROM vehicles`,
		"users":              `SELECT COUNT(*) FROM users WHERE role <> 'platform_owner'`,
	}
	for k, q := range queries {
		var n int
		if err := d.DB.Pool.QueryRow(r.Context(), q).Scan(&n); err != nil {
			writeErr(w, http.StatusInternalServerError, "db error")
			return
		}
		stats[k] = n
	}
	writeJSON(w, http.StatusOK, stats)
}

// ListPlans: public, since the signup flow needs it.
func (d *Deps) ListPlans(w http.ResponseWriter, r *http.Request) {
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, code, name, monthly_price_cents, annual_price_cents,
		       max_users, max_vehicles, max_branches, features
		FROM subscription_plans ORDER BY monthly_price_cents
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type plan struct {
		ID                uuid.UUID   `json:"id"`
		Code              string      `json:"code"`
		Name              string      `json:"name"`
		MonthlyPriceCents int         `json:"monthlyPriceCents"`
		AnnualPriceCents  int         `json:"annualPriceCents"`
		MaxUsers          *int        `json:"maxUsers"`
		MaxVehicles       *int        `json:"maxVehicles"`
		MaxBranches       *int        `json:"maxBranches"`
		Features          interface{} `json:"features"`
	}
	out := []plan{}
	for rows.Next() {
		var p plan
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.MonthlyPriceCents, &p.AnnualPriceCents, &p.MaxUsers, &p.MaxVehicles, &p.MaxBranches, &p.Features); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}
