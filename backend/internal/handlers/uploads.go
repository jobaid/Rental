package handlers

import (
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// VehiclePhotos: list.
func (d *Deps) ListVehiclePhotos(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	vid := chi.URLParam(r, "id")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, kind, caption, file_path, is_primary, display_order, created_at::text
		FROM vehicle_photos WHERE company_id=$1 AND vehicle_id=$2
		ORDER BY is_primary DESC, display_order ASC, created_at ASC
	`, c.CompanyID, vid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type p struct {
		ID       uuid.UUID `json:"id"`
		Kind     *string   `json:"kind"`
		Caption  *string   `json:"caption"`
		URL      string    `json:"url"`
		Primary  bool      `json:"isPrimary"`
		Order    int       `json:"displayOrder"`
		Created  string    `json:"createdAt"`
	}
	out := []p{}
	for rows.Next() {
		var row p
		var file string
		if err := rows.Scan(&row.ID, &row.Kind, &row.Caption, &file, &row.Primary, &row.Order, &row.Created); err == nil {
			row.URL = "/uploads/" + file
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// VehiclePhotos: upload multipart.
func (d *Deps) UploadVehiclePhoto(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	vid := chi.URLParam(r, "id")
	// Make sure the vehicle belongs to this tenant.
	var ok bool
	err := d.DB.Pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM vehicles WHERE id=$1 AND company_id=$2)`, vid, c.CompanyID).Scan(&ok)
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "vehicle not found")
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart parse: "+err.Error())
		return
	}
	kind := r.FormValue("kind")
	caption := r.FormValue("caption")
	primary := r.FormValue("primary") == "true"

	fh, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	fh.Close()
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	rel, err := d.Store.Save(headers[0], path.Join("vehicles", vid))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "save failed: "+err.Error())
		return
	}
	if primary {
		_, _ = d.DB.Pool.Exec(r.Context(),
			`UPDATE vehicle_photos SET is_primary=FALSE WHERE company_id=$1 AND vehicle_id=$2`,
			c.CompanyID, vid)
	}
	var id uuid.UUID
	err = d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO vehicle_photos (company_id, vehicle_id, kind, caption, file_path, is_primary, uploaded_by)
		VALUES ($1,$2::uuid,NULLIF($3,''),NULLIF($4,''),$5,$6,$7)
		RETURNING id
	`, c.CompanyID, vid, kind, caption, rel, primary, c.UserID).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "url": "/uploads/" + rel})
}

func (d *Deps) DeleteVehiclePhoto(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "pid")
	_, err := d.DB.Pool.Exec(r.Context(),
		`DELETE FROM vehicle_photos WHERE company_id=$1 AND id=$2`, c.CompanyID, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Public (unauthenticated) vehicle-photo listing — used by the booking engine.
func (d *Deps) PublicVehiclePhotos(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT file_path, is_primary FROM vehicle_photos
		WHERE vehicle_id=$1 ORDER BY is_primary DESC, display_order ASC
	`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		var pri bool
		if err := rows.Scan(&p, &pri); err == nil {
			out = append(out, "/uploads/"+p)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Generic customer-document upload (admin side).
func (d *Deps) UploadCustomerDocument(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	cid := chi.URLParam(r, "id")
	var ok bool
	_ = d.DB.Pool.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM customers WHERE id=$1 AND company_id=$2)`,
		cid, c.CompanyID).Scan(&ok)
	if !ok {
		writeErr(w, http.StatusNotFound, "customer not found")
		return
	}
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "multipart parse")
		return
	}
	kind := r.FormValue("kind")
	if kind == "" {
		kind = "other"
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 {
		writeErr(w, http.StatusBadRequest, "file required")
		return
	}
	rel, err := d.Store.Save(headers[0], path.Join("customers", cid))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "save failed: "+err.Error())
		return
	}
	var id uuid.UUID
	err = d.DB.Pool.QueryRow(r.Context(), `
		INSERT INTO customer_documents (company_id, customer_id, kind, file_path, status)
		VALUES ($1,$2::uuid,$3,$4,'pending') RETURNING id
	`, c.CompanyID, cid, kind, rel).Scan(&id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "url": "/uploads/" + rel})
}

func (d *Deps) ListCustomerDocuments(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	cid := chi.URLParam(r, "id")
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT id, kind, file_path, status, created_at::text
		FROM customer_documents WHERE company_id=$1 AND customer_id=$2 ORDER BY created_at DESC
	`, c.CompanyID, cid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type doc struct {
		ID        uuid.UUID `json:"id"`
		Kind      string    `json:"kind"`
		URL       string    `json:"url"`
		Status    string    `json:"status"`
		CreatedAt string    `json:"createdAt"`
	}
	out := []doc{}
	for rows.Next() {
		var row doc
		var fp string
		if err := rows.Scan(&row.ID, &row.Kind, &fp, &row.Status, &row.CreatedAt); err == nil {
			row.URL = "/uploads/" + fp
			out = append(out, row)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (d *Deps) VerifyCustomerDocument(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	id := chi.URLParam(r, "docId")
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Status != "verified" && body.Status != "rejected" && body.Status != "pending" {
		writeErr(w, http.StatusBadRequest, "status must be pending|verified|rejected")
		return
	}
	_, err := d.DB.Pool.Exec(r.Context(), `
		UPDATE customer_documents
		   SET status=$1, note=NULLIF($2,''), verified_by=$3, verified_at=NOW()
		 WHERE company_id=$4 AND id=$5
	`, body.Status, body.Note, c.UserID, c.CompanyID, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
