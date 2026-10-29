package web

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (h *Handler) About(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/about.html", "About · mosrental", "", nil)
}
func (h *Handler) FAQ(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/faq.html", "FAQ · mosrental", "", nil)
}
func (h *Handler) Terms(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/terms.html", "Terms & conditions · mosrental", "", nil)
}
func (h *Handler) Legal(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/legal.html", "Legal & privacy · mosrental", "", nil)
}
func (h *Handler) Contact(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "public/contact.html", "Contact · mosrental", "", map[string]any{})
}

// ContactPost records a contact message as a notification (so admin can see it in the queue).
func (h *Handler) ContactPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, r, "public/contact.html", "Contact", "", map[string]any{"Err": "Please fill the form again."})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	subject := r.FormValue("subject")
	bookingNum := r.FormValue("bookingNumber")
	msg := strings.TrimSpace(r.FormValue("message"))

	if name == "" || email == "" || msg == "" {
		h.render(w, r, "public/contact.html", "Contact", "", map[string]any{"Err": "Name, email and message are required."})
		return
	}
	// Find the single company (if any) to attach the notification to.
	var cid uuid.UUID
	_ = h.Pool.QueryRow(r.Context(), `SELECT id FROM companies ORDER BY created_at LIMIT 1`).Scan(&cid)

	body := "From: " + name + " <" + email + ">\nSubject: " + subject + "\nBooking: " + bookingNum + "\n\n" + msg
	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO notifications (company_id, kind, recipient, subject, body, status)
		VALUES ($1, 'contact_form', $2, $3, $4, 'queued')
	`, cid, email, "Contact form: "+subject, body)

	h.render(w, r, "public/contact.html", "Thanks · mosrental", "", map[string]any{"Submitted": true})
}
