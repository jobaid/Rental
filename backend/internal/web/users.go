package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rentalsoftware/backend/internal/auth"
	"github.com/rentalsoftware/backend/internal/mail"
)

type StaffUser struct {
	ID          uuid.UUID
	Email       string
	Name        string
	Role        string
	IsActive    bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

var staffRoles = []struct {
	Code, Label string
}{
	{"company_owner", "Company owner (full access)"},
	{"rental_manager", "Manager"},
	{"branch_manager", "Branch manager"},
	{"reservation_agent", "Reservation agent (Sales)"},
	{"fleet_manager", "Fleet manager"},
	{"technician", "Technician / maintenance"},
	{"accountant", "Accountant"},
	{"auditor", "Read-only auditor"},
}

func canManageUsers(s Session) bool {
	return s.Role == "company_owner" || s.Role == "rental_manager" || s.Role == "platform_owner"
}

func (h *Handler) UsersList(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if !canManageUsers(s) {
		http.Redirect(w, r, "/admin?err=Not+authorized", http.StatusSeeOther)
		return
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, email, name, role, is_active, last_login_at, created_at
		FROM users
		WHERE company_id=$1 AND role <> 'customer'
		ORDER BY (role='company_owner') DESC, name
	`, cid)
	list := []StaffUser{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var u StaffUser
			if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.IsActive, &u.LastLoginAt, &u.CreatedAt); err == nil {
				list = append(list, u)
			}
		}
	}
	h.render(w, r, "admin/users.html", "Users", "users", map[string]any{
		"Users": list,
		"Roles": staffRoles,
		"Flash": r.URL.Query().Get("flash"),
		"Err":   r.URL.Query().Get("err"),
	})
}

func (h *Handler) UserCreate(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if !canManageUsers(s) {
		http.Redirect(w, r, "/admin/users?err=Not+authorized", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	role := r.FormValue("role")
	pw := r.FormValue("password")

	if name == "" || !mail.ValidateAddress(email) || len(pw) < 8 {
		http.Redirect(w, r, "/admin/users?err=Name,+valid+email,+and+password+(>=8+chars)+required", http.StatusSeeOther)
		return
	}
	ok := false
	for _, rr := range staffRoles {
		if rr.Code == role {
			ok = true
			break
		}
	}
	if !ok {
		http.Redirect(w, r, "/admin/users?err=Invalid+role", http.StatusSeeOther)
		return
	}
	hash, _ := auth.HashPassword(pw)
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO users (company_id, email, password_hash, name, role, is_active, email_verified_at)
		VALUES ($1,$2,$3,$4,$5,TRUE,NOW())
	`, cid, email, hash, name, role)
	if err != nil {
		http.Redirect(w, r, "/admin/users?err=Could+not+add+(email+already+in+use?)", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?flash=User+added", http.StatusSeeOther)
}

func (h *Handler) UserToggle(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if !canManageUsers(s) {
		http.Redirect(w, r, "/admin/users?err=Not+authorized", http.StatusSeeOther)
		return
	}
	id := chi.URLParam(r, "id")
	if id == s.UserID.String() {
		http.Redirect(w, r, "/admin/users?err=You+can't+disable+your+own+account", http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `
		UPDATE users SET is_active = NOT is_active, updated_at=NOW()
		WHERE company_id=$1 AND id=$2
	`, cid, id)
	http.Redirect(w, r, "/admin/users?flash=User+updated", http.StatusSeeOther)
}

func (h *Handler) UserChangeRole(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if !canManageUsers(s) {
		http.Redirect(w, r, "/admin/users?err=Not+authorized", http.StatusSeeOther)
		return
	}
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	role := r.FormValue("role")
	ok := false
	for _, rr := range staffRoles {
		if rr.Code == role {
			ok = true
			break
		}
	}
	if !ok {
		http.Redirect(w, r, "/admin/users?err=Invalid+role", http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET role=$1, updated_at=NOW() WHERE company_id=$2 AND id=$3`, role, cid, id)
	http.Redirect(w, r, "/admin/users?flash=Role+updated", http.StatusSeeOther)
}

func (h *Handler) UserResetPassword(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if !canManageUsers(s) {
		http.Redirect(w, r, "/admin/users?err=Not+authorized", http.StatusSeeOther)
		return
	}
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	pw := r.FormValue("password")
	if len(pw) < 8 {
		http.Redirect(w, r, "/admin/users?err=Password+must+be+at+least+8+chars", http.StatusSeeOther)
		return
	}
	hash, _ := auth.HashPassword(pw)
	_, _ = h.Pool.Exec(r.Context(), `UPDATE users SET password_hash=$1, updated_at=NOW() WHERE company_id=$2 AND id=$3`, hash, cid, id)
	http.Redirect(w, r, "/admin/users?flash=Password+reset", http.StatusSeeOther)
}
