package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rentalsoftware/backend/internal/auth"
)

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	// Optional: when a platform owner logs in they omit company; a tenant user supplies company slug if we want to disambiguate.
	CompanySlug string `json:"companySlug,omitempty"`
}

type loginResp struct {
	Token     string     `json:"token"`
	UserID    uuid.UUID  `json:"userId"`
	Role      string     `json:"role"`
	CompanyID *uuid.UUID `json:"companyId,omitempty"`
	Name      string     `json:"name"`
}

func (d *Deps) Login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "email and password required")
		return
	}

	ctx := r.Context()
	var (
		id        uuid.UUID
		companyID *uuid.UUID
		hash      string
		role      string
		name      string
		isActive  bool
	)

	// If a company slug is given, scope the lookup; else try platform owner first, then any company user by email.
	if req.CompanySlug != "" {
		err := d.DB.Pool.QueryRow(ctx, `
			SELECT u.id, u.company_id, u.password_hash, u.role, u.name, u.is_active
			FROM users u JOIN companies c ON c.id = u.company_id
			WHERE c.slug = $1 AND u.email = $2
		`, req.CompanySlug, req.Email).Scan(&id, &companyID, &hash, &role, &name, &isActive)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
	} else {
		err := d.DB.Pool.QueryRow(ctx, `
			SELECT id, company_id, password_hash, role, name, is_active
			FROM users WHERE email = $1 AND company_id IS NULL
		`, req.Email).Scan(&id, &companyID, &hash, &role, &name, &isActive)
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	if !isActive || !auth.VerifyPassword(hash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	tok, err := d.Signer.Sign(id, companyID, role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sign error")
		return
	}

	_, _ = d.DB.Pool.Exec(ctx, `UPDATE users SET last_login_at = NOW() WHERE id = $1`, id)
	writeJSON(w, http.StatusOK, loginResp{Token: tok, UserID: id, Role: role, CompanyID: companyID, Name: name})
}

type bootstrapReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// BootstrapOwner creates the FIRST platform owner. Succeeds only when zero platform_owners exist.
// Idempotent-safe: subsequent calls return 403.
func (d *Deps) BootstrapOwner(w http.ResponseWriter, r *http.Request) {
	var req bootstrapReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" || len(req.Password) < 8 || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name, email and password (>=8 chars) required")
		return
	}

	ctx := r.Context()
	tx, err := d.DB.Pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'platform_owner'`).Scan(&count); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if count > 0 {
		writeErr(w, http.StatusForbidden, "platform owner already exists")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hash error")
		return
	}

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO users (company_id, email, password_hash, name, role, email_verified_at)
		VALUES (NULL, $1, $2, $3, 'platform_owner', NOW())
		RETURNING id
	`, req.Email, hash, req.Name).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit error")
		return
	}

	tok, _ := d.Signer.Sign(id, nil, "platform_owner")
	writeJSON(w, http.StatusCreated, loginResp{Token: tok, UserID: id, Role: "platform_owner", Name: req.Name})
}

// Me returns the current user from the token.
func (d *Deps) Me(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	if c == nil {
		writeErr(w, http.StatusUnauthorized, "no claims")
		return
	}
	var name string
	var role string
	ctxTimeout, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	err := d.DB.Pool.QueryRow(ctxTimeout, `SELECT name, role FROM users WHERE id=$1`, c.UserID).Scan(&name, &role)
	if err != nil {
		writeErr(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userId":    c.UserID,
		"companyId": c.CompanyID,
		"role":      role,
		"name":      name,
	})
}
