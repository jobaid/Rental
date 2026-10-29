package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type CustomerDetail struct {
	ID            uuid.UUID
	FirstName     string
	LastName      string
	Email         *string
	Phone         *string
	Address       *string
	Street2       *string
	City          *string
	State         *string
	Zip           *string
	Country       *string
	LicenseNumber *string
	LicenseExpiry *string
	Birthday      *string
	Notes         *string
	CreatedAt     time.Time
}

type CustomerRelation struct {
	ID             uuid.UUID
	RelatedContact string
	Relation       string
}

type ReservationBrief struct {
	ID            uuid.UUID
	BookingNumber string
	Status        string
	PickupAt      time.Time
	ReturnAt      time.Time
	TotalCents    int
	PaidCents     int
	Vehicle       string
}

type AuditEvent struct {
	ID        int64
	Action    string
	Actor     *string
	CreatedAt time.Time
}

type FileRow struct {
	ID        uuid.UUID
	Kind      string
	URL       string
	Status    string
	CreatedAt time.Time
}

// CustomerDetailPage renders the full customer detail with tabs.
func (h *Handler) CustomerDetailPage(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "contact"
	}

	var c CustomerDetail
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, first_name, last_name, email, phone, address, street2, city, state, zip, country,
		       license_number, license_expiry::text, birthday::text, notes, created_at
		FROM customers WHERE company_id=$1 AND id=$2
	`, cid, id).Scan(&c.ID, &c.FirstName, &c.LastName, &c.Email, &c.Phone, &c.Address, &c.Street2,
		&c.City, &c.State, &c.Zip, &c.Country, &c.LicenseNumber, &c.LicenseExpiry,
		&c.Birthday, &c.Notes, &c.CreatedAt)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	data := map[string]any{
		"C":     c,
		"Tab":   tab,
		"Flash": r.URL.Query().Get("flash"),
		"Err":   r.URL.Query().Get("err"),
	}

	// Relations (right panel).
	data["Relations"] = h.loadRelations(r, cid, c.ID)

	// Reservations count + alerts for the sidebar/tabs.
	var resCount, overdueCount, quoteCount, fileCount int
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND customer_id=$2`, cid, c.ID).Scan(&resCount)
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND customer_id=$2 AND status='checked_out' AND return_at < NOW()`,
		cid, c.ID).Scan(&overdueCount)
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND customer_id=$2 AND status='pending'`, cid, c.ID).Scan(&quoteCount)
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM customer_documents WHERE company_id=$1 AND customer_id=$2`, cid, c.ID).Scan(&fileCount)
	data["ReservationsCount"] = resCount
	data["QuotesCount"] = quoteCount
	data["OverdueCount"] = overdueCount
	data["FilesCount"] = fileCount
	data["IsNewCustomer"] = resCount == 0

	// Per-tab lazy loads.
	switch tab {
	case "reservations", "quotes":
		rows, err := h.Pool.Query(r.Context(), `
			SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at, r.total_cents, r.paid_cents,
			       CASE WHEN v.id IS NULL THEN '' ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END
			FROM reservations r LEFT JOIN vehicles v ON v.id=r.vehicle_id
			WHERE r.company_id=$1 AND r.customer_id=$2 `+
			map[string]string{"reservations": "", "quotes": "AND r.status='pending'"}[tab]+`
			ORDER BY r.pickup_at DESC LIMIT 200
		`, cid, c.ID)
		list := []ReservationBrief{}
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var rr ReservationBrief
				if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Status, &rr.PickupAt, &rr.ReturnAt, &rr.TotalCents, &rr.PaidCents, &rr.Vehicle); err == nil {
					list = append(list, rr)
				}
			}
		}
		data["Reservations"] = list

	case "alerts":
		// Simple alert list: overdue bookings + expired license.
		type Alert struct{ Kind, Message, Href string }
		alerts := []Alert{}
		if c.LicenseExpiry != nil {
			if t, err := time.Parse("2006-01-02", *c.LicenseExpiry); err == nil {
				if t.Before(time.Now()) {
					alerts = append(alerts, Alert{"license_expired", "Driver license expired on " + *c.LicenseExpiry, ""})
				} else if t.Before(time.Now().AddDate(0, 1, 0)) {
					alerts = append(alerts, Alert{"license_expiring", "Driver license expires on " + *c.LicenseExpiry, ""})
				}
			}
		}
		if overdueCount > 0 {
			alerts = append(alerts, Alert{"overdue", "Customer has an overdue rental", "?tab=reservations"})
		}
		data["Alerts"] = alerts

	case "files":
		rows, _ := h.Pool.Query(r.Context(), `
			SELECT id, kind, file_path, status, created_at
			FROM customer_documents WHERE company_id=$1 AND customer_id=$2 ORDER BY created_at DESC
		`, cid, c.ID)
		files := []FileRow{}
		if rows != nil {
			for rows.Next() {
				var f FileRow
				var fp string
				if err := rows.Scan(&f.ID, &f.Kind, &fp, &f.Status, &f.CreatedAt); err == nil {
					f.URL = "/uploads/" + fp
					files = append(files, f)
				}
			}
			rows.Close()
		}
		data["Files"] = files

	case "audit":
		rows, _ := h.Pool.Query(r.Context(), `
			SELECT ae.id, ae.action, u.name, ae.created_at
			FROM audit_events ae LEFT JOIN users u ON u.id=ae.actor_user_id
			WHERE ae.company_id=$1 AND (ae.target_type='customer' AND ae.target_id=$2)
			ORDER BY ae.created_at DESC LIMIT 100
		`, cid, c.ID.String())
		events := []AuditEvent{}
		if rows != nil {
			for rows.Next() {
				var a AuditEvent
				if err := rows.Scan(&a.ID, &a.Action, &a.Actor, &a.CreatedAt); err == nil {
					events = append(events, a)
				}
			}
			rows.Close()
		}
		data["Audit"] = events
	}

	h.render(w, r, "admin/customer_detail.html", "Customer · "+c.FirstName+" "+c.LastName, "customers", data)
}

func (h *Handler) loadRelations(r *http.Request, cid, custID uuid.UUID) []CustomerRelation {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, related_contact, relation FROM customer_relations
		WHERE company_id=$1 AND customer_id=$2 ORDER BY created_at
	`, cid, custID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []CustomerRelation{}
	for rows.Next() {
		var cr CustomerRelation
		if err := rows.Scan(&cr.ID, &cr.RelatedContact, &cr.Relation); err == nil {
			out = append(out, cr)
		}
	}
	return out
}

// CustomerUpdate: save fields from the Contact tab.
func (h *Handler) CustomerUpdate(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	f := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	_, err := h.Pool.Exec(r.Context(), `
		UPDATE customers SET
		  first_name=$1, last_name=$2,
		  email=NULLIF(LOWER($3),''), phone=NULLIF($4,''),
		  address=NULLIF($5,''), street2=NULLIF($6,''), city=NULLIF($7,''),
		  state=NULLIF($8,''), zip=NULLIF($9,''), country=NULLIF($10,''),
		  license_number=NULLIF($11,''), license_expiry=NULLIF($12,'')::date,
		  birthday=NULLIF($13,'')::date,
		  updated_at=NOW()
		WHERE company_id=$14 AND id=$15
	`, f("firstName"), f("lastName"), f("email"), f("phone"),
		f("address"), f("street2"), f("city"), f("state"), f("zip"), f("country"),
		f("licenseNumber"), f("licenseExpiry"), f("birthday"),
		cid, id)
	if err != nil {
		http.Redirect(w, r, "/admin/customers/"+id+"?err="+err.Error(), http.StatusSeeOther)
		return
	}
	// Audit.
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO audit_events (company_id, actor_user_id, action, target_type, target_id) VALUES ($1,$2,'customer.update','customer',$3)`,
		cid, s.UserID, id)
	http.Redirect(w, r, "/admin/customers/"+id+"?flash=Saved", http.StatusSeeOther)
}

func (h *Handler) CustomerDelete(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	// Soft-delete: there's no deleted_at column, so block delete if customer has reservations.
	var n int
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM reservations WHERE customer_id=$1`, id).Scan(&n)
	if n > 0 {
		http.Redirect(w, r, "/admin/customers/"+id+"?err=Can%27t+delete+%28customer+has+"+itoaInt(n)+"+bookings%29", http.StatusSeeOther)
		return
	}
	_, err := h.Pool.Exec(r.Context(), `DELETE FROM customers WHERE company_id=$1 AND id=$2`, cid, id)
	if err != nil {
		http.Redirect(w, r, "/admin/customers/"+id+"?err=Delete+failed", http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO audit_events (company_id, actor_user_id, action, target_type, target_id) VALUES ($1,$2,'customer.delete','customer',$3)`,
		cid, s.UserID, id)
	http.Redirect(w, r, "/admin/customers?flash=Customer+deleted", http.StatusSeeOther)
}

func (h *Handler) CustomerRelationAdd(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	contact := strings.TrimSpace(r.FormValue("relatedContact"))
	rel := strings.TrimSpace(r.FormValue("relation"))
	if contact == "" || rel == "" {
		http.Redirect(w, r, "/admin/customers/"+id+"?err=Both+fields+required", http.StatusSeeOther)
		return
	}
	_, _ = h.Pool.Exec(r.Context(),
		`INSERT INTO customer_relations (company_id, customer_id, related_contact, relation) VALUES ($1,$2::uuid,$3,$4)`,
		cid, id, contact, rel)
	http.Redirect(w, r, "/admin/customers/"+id+"?flash=Relation+added", http.StatusSeeOther)
}

func (h *Handler) CustomerRelationDelete(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	rid := chi.URLParam(r, "rid")
	_, _ = h.Pool.Exec(r.Context(),
		`DELETE FROM customer_relations WHERE company_id=$1 AND customer_id=$2 AND id=$3`, cid, id, rid)
	http.Redirect(w, r, "/admin/customers/"+id+"?flash=Relation+removed", http.StatusSeeOther)
}

func (h *Handler) CustomerNotesSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	notes := strings.TrimSpace(r.FormValue("notes"))
	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE customers SET notes=NULLIF($1,''), updated_at=NOW() WHERE company_id=$2 AND id=$3`,
		notes, cid, id)
	http.Redirect(w, r, "/admin/customers/"+id+"?tab=notes&flash=Notes+saved", http.StatusSeeOther)
}
