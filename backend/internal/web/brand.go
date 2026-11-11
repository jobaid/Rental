package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strings"
	"time"

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
	Name     string
	LogoURL  string
	HeroURL  string // optional MP4/WebM/GIF/image shown in the homepage hero instead of the SVG
	HeroKind string // "video" (MP4/WebM) or "image" (GIF/PNG/JPG/WebP)
	Phone    string
	Email    string
	Address  string
	Hours    string
	Website  string
	FooterHTML string // admin-editable bottom-of-page line; HTML allowed
	ShowStaffLogin bool // show "Staff sign in" button in public header
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
	b.HeroURL = str("heroUrl")
	// Infer type from extension so the template picks <video> vs <img>.
	lower := strings.ToLower(b.HeroURL)
	switch {
	case strings.HasSuffix(lower, ".mp4"), strings.HasSuffix(lower, ".webm"), strings.HasSuffix(lower, ".mov"):
		b.HeroKind = "video"
	case strings.HasSuffix(lower, ".gif"), strings.HasSuffix(lower, ".png"), strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"), strings.HasSuffix(lower, ".webp"):
		b.HeroKind = "image"
	}
	b.Phone = str("phone")
	b.Email = str("email")
	b.Address = str("address")
	b.Hours = str("hours")
	b.Website = str("website")
	b.FooterHTML = str("footerHtml")
	if v, ok := m["showStaffLogin"].(bool); ok {
		b.ShowStaffLogin = v
	}
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

// HeroUpload accepts a hero image or video for the homepage animation.
// Supports MP4 / WebM / MOV (served as <video>), and GIF / PNG / JPG / WebP
// (served as <img>). 20 MB cap.
func (h *Handler) HeroUpload(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		http.Redirect(w, r, "/admin/settings?err=Bad+upload", http.StatusSeeOther)
		return
	}
	hdrs := r.MultipartForm.File["hero"]
	if len(hdrs) == 0 {
		http.Redirect(w, r, "/admin/settings?err=File+required", http.StatusSeeOther)
		return
	}
	rel, err := h.Store.Save(hdrs[0], path.Join("hero", cid.String()))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error(), http.StatusSeeOther)
		return
	}
	url := "/uploads/" + rel

	var raw []byte
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid).Scan(&raw)
	m := map[string]any{}
	if len(raw) > 0 {
		_ = jsonUnmarshal(raw, &m)
	}
	m["heroUrl"] = url
	b, _ := jsonMarshal(m)
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'general',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?flash=Hero+animation+updated", http.StatusSeeOther)
}

// HeroRemove clears the hero URL.
func (h *Handler) HeroRemove(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	var raw []byte
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid).Scan(&raw)
	m := map[string]any{}
	if len(raw) > 0 {
		_ = jsonUnmarshal(raw, &m)
	}
	delete(m, "heroUrl")
	b, _ := jsonMarshal(m)
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'general',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	http.Redirect(w, r, "/admin/settings?flash=Hero+animation+removed", http.StatusSeeOther)
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

// brandFooterHTML expands {{YEAR}} and {{COMPANY}} placeholders in the
// admin-authored footer HTML and returns it as template.HTML so tags
// render instead of being escaped. Called from the public layout only.
func brandFooterHTML(b Brand) template.HTML {
	raw := b.FooterHTML
	raw = strings.ReplaceAll(raw, "{{YEAR}}", fmt.Sprintf("%d", time.Now().Year()))
	raw = strings.ReplaceAll(raw, "{{COMPANY}}", b.Name)
	raw = strings.ReplaceAll(raw, "{{PHONE}}", b.Phone)
	raw = strings.ReplaceAll(raw, "{{EMAIL}}", b.Email)
	raw = strings.ReplaceAll(raw, "{{ADDRESS}}", b.Address)
	raw = strings.ReplaceAll(raw, "{{HOURS}}", b.Hours)
	raw = strings.ReplaceAll(raw, "{{WEBSITE}}", b.Website)
	return template.HTML(raw)
}

// Unused — suppresses the strings import warning if nothing else needs it.
var _ = strings.TrimSpace
