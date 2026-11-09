package web

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/google/uuid"
)

// =============================================================================
// Brand — company name, logo URL, and contact info surfaced on public pages.
//
// Reads settings.general (key='general'). The admin edits these via Settings,
// including a dedicated logo upload. render() loads the Brand for every
// public page via the loadBrand hook so the header/footer/contact page all
// show consistent info without each handler having to pass it.
// =============================================================================

type Brand struct {
	Name    string
	LogoURL string
	Phone   string
	Email   string
	Address string
	Hours   string
	Website string
}

// loadBrand returns the first active company's branding (single-tenant mode).
func (h *Handler) loadBrand(ctx context.Context) Brand {
	b := Brand{Name: "mosrental"}
	// Prefer companies.name as canonical; settings.general overrides visible bits.
	var cid uuid.UUID
	var companyName string
	if err := h.Pool.QueryRow(ctx,
		`SELECT id, name FROM companies WHERE status='active' ORDER BY created_at LIMIT 1`,
	).Scan(&cid, &companyName); err == nil && companyName != "" {
		b.Name = companyName
	}
	if cid == uuid.Nil {
		return b
	}
	var raw []byte
	if err := h.Pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid,
	).Scan(&raw); err != nil {
		return b
	}
	m := map[string]any{}
	if jsonUnmarshal(raw, &m) != nil {
		return b
	}
	str := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	if v := str("businessName"); v != "" {
		b.Name = v
	}
	b.LogoURL = str("logoUrl")
	b.Phone = str("phone")
	b.Email = str("email")
	b.Address = str("address")
	b.Hours = str("hours")
	b.Website = str("website")
	return b
}

// LogoUpload handles multipart upload of a company logo. Saves to the shared
// uploads storage and stores the served URL (/uploads/logos/<file>) into
// settings.general.logoUrl. POST /admin/settings/logo
func (h *Handler) LogoUpload(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		http.Redirect(w, r, "/admin/settings?err=Bad+upload", http.StatusSeeOther)
		return
	}
	hdrs := r.MultipartForm.File["logo"]
	if len(hdrs) == 0 {
		http.Redirect(w, r, "/admin/settings?err=File+required", http.StatusSeeOther)
		return
	}
	rel, err := h.Store.Save(hdrs[0], path.Join("logos", cid.String()))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error(), http.StatusSeeOther)
		return
	}
	url := "/uploads/" + rel

	// Load-modify-save settings.general so other fields aren't wiped.
	var raw []byte
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid).Scan(&raw)
	m := map[string]any{}
	if len(raw) > 0 {
		_ = jsonUnmarshal(raw, &m)
	}
	m["logoUrl"] = url
	b, _ := jsonMarshal(m)
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'general',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?flash=Logo+updated", http.StatusSeeOther)
}

// LogoRemove clears the logo URL without touching other settings.
func (h *Handler) LogoRemove(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	var raw []byte
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid).Scan(&raw)
	m := map[string]any{}
	if len(raw) > 0 {
		_ = jsonUnmarshal(raw, &m)
	}
	delete(m, "logoUrl")
	b, _ := jsonMarshal(m)
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'general',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	http.Redirect(w, r, "/admin/settings?flash=Logo+removed", http.StatusSeeOther)
}

// Unused — suppresses the strings import warning if nothing else needs it.
var _ = strings.TrimSpace
