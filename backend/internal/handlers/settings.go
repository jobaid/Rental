package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// GetSettings returns {key: value} for this company; if `?key=general` passed, returns a single value.
func (d *Deps) GetSettings(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	k := strings.TrimSpace(r.URL.Query().Get("key"))
	if k != "" {
		var raw json.RawMessage
		err := d.DB.Pool.QueryRow(r.Context(),
			`SELECT value FROM settings WHERE company_id=$1 AND key=$2`, c.CompanyID, k).Scan(&raw)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}
	rows, err := d.DB.Pool.Query(r.Context(),
		`SELECT key, value FROM settings WHERE company_id=$1 ORDER BY key`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k string
		var v json.RawMessage
		if err := rows.Scan(&k, &v); err == nil {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type setSetReq struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (d *Deps) SetSetting(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req setSetReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Key == "" || len(req.Value) == 0 {
		writeErr(w, http.StatusBadRequest, "key and value required")
		return
	}
	// Validate JSON.
	var tmp any
	if err := json.Unmarshal(req.Value, &tmp); err != nil {
		writeErr(w, http.StatusBadRequest, "value must be valid JSON")
		return
	}
	_, err := d.DB.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value, updated_by)
		VALUES ($1,$2,$3::jsonb,$4)
		ON CONFLICT (company_id, key) DO UPDATE
		   SET value=EXCLUDED.value, updated_by=EXCLUDED.updated_by, updated_at=NOW()
	`, c.CompanyID, req.Key, string(req.Value), c.UserID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "upsert failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Public read of some settings so the customer site can show business info.
func (d *Deps) PublicSettings(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var cid uuid.UUID
	err := d.DB.Pool.QueryRow(r.Context(), `SELECT id FROM companies WHERE slug=$1 AND status='active'`, slug).Scan(&cid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "company not found")
		return
	}
	var raw json.RawMessage
	err = d.DB.Pool.QueryRow(r.Context(),
		`SELECT value FROM settings WHERE company_id=$1 AND key='general'`, cid).Scan(&raw)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

// Custom fields.
type cfReq struct {
	Entity       string          `json:"entity"`
	Name         string          `json:"name"`
	Label        string          `json:"label"`
	Kind         string          `json:"kind"`
	Options      json.RawMessage `json:"options"`
	Required     bool            `json:"required"`
	DisplayOrder int             `json:"displayOrder"`
}

func (d *Deps) ListCustomFields(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	entity := strings.TrimSpace(r.URL.Query().Get("entity"))
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, entity, name, label, kind, options, required, display_order, is_active
		FROM custom_fields
		WHERE company_id=$1 AND ($2='' OR entity=$2) AND is_active=TRUE
		ORDER BY entity, display_order, label
	`, c.CompanyID, entity)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type f struct {
		ID       uuid.UUID       `json:"id"`
		Entity   string          `json:"entity"`
		Name     string          `json:"name"`
		Label    string          `json:"label"`
		Kind     string          `json:"kind"`
		Options  json.RawMessage `json:"options"`
		Required bool            `json:"required"`
		Order    int             `json:"displayOrder"`
		Active   bool            `json:"isActive"`
	}
	out := []f{}
	for rows.Next() {
		var row f
		if err := rows.Scan(&row.ID, &row.Entity, &row.Name, &row.Label, &row.Kind, &row.Options, &row.Required, &row.Order, &row.Active); err == nil {
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) CreateCustomField(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	var req cfReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Entity == "" || req.Name == "" || req.Label == "" || req.Kind == "" {
		writeErr(w, http.StatusBadRequest, "entity, name, label, kind required")
		return
	}
	opts := string(req.Options)
	if opts == "" {
		opts = "null"
	}
	var id uuid.UUID
	err := d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO custom_fields (company_id, entity, name, label, kind, options, required, display_order)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8) RETURNING id
	`, c.CompanyID, req.Entity, req.Name, req.Label, req.Kind, opts, req.Required, req.DisplayOrder).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "insert failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}
