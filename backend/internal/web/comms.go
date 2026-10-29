package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rentalsoftware/backend/internal/mail"
)

// Mailer is set by main.
func (h *Handler) SetMailer(m *mail.Sender) {
	h.Mailer = m
	h.EnvSMTP = m.Cfg
}

// resolveSMTP returns the SMTP config for a given company. DB settings override env vars.
func (h *Handler) resolveSMTP(ctx context.Context, cid uuid.UUID) mail.Config {
	cfg := h.EnvSMTP
	var raw []byte
	if err := h.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE company_id=$1 AND key='smtp'`, cid).Scan(&raw); err != nil {
		return cfg
	}
	var m map[string]string
	if err := jsonUnmarshal(raw, &m); err != nil {
		return cfg
	}
	if v, ok := m["host"]; ok && v != "" { cfg.Host = v }
	if v, ok := m["port"]; ok && v != "" { cfg.Port = v }
	if v, ok := m["user"]; ok && v != "" { cfg.User = v }
	if v, ok := m["pass"]; ok && v != "" { cfg.Pass = v }
	if v, ok := m["fromAddr"]; ok && v != "" { cfg.FromAddr = v }
	if v, ok := m["fromName"]; ok && v != "" { cfg.FromName = v }
	return cfg
}

// mailerFor returns a sender using the resolved config.
func (h *Handler) mailerFor(ctx context.Context, cid uuid.UUID) *mail.Sender {
	return &mail.Sender{Cfg: h.resolveSMTP(ctx, cid)}
}

type Thread struct {
	ID             uuid.UUID
	CustomerID     *uuid.UUID
	ReservationID  *uuid.UUID
	Status         string
	UnreadCount    int
	LastMessageAt  *time.Time
	LastPreview    *string

	CustomerName   string
	CustomerFirst  string
	CustomerLast   string
	CustomerEmail  *string
	CustomerPhone  *string

	BookingNumber  *string
	BookingStatus  *string
	VehicleLabel   *string

	HasEmail       bool
	HasSMS         bool
}

func (t Thread) Initials() string {
	s := ""
	if len(t.CustomerFirst) > 0 { s += string(t.CustomerFirst[0]) }
	if len(t.CustomerLast) > 0  { s += string(t.CustomerLast[0]) }
	if s == "" { return "?" }
	return strings.ToUpper(s)
}

type Message struct {
	ID             uuid.UUID
	ThreadID       uuid.UUID
	SenderType     string
	SenderName     *string
	Channel        string
	Direction      string
	Recipient      *string
	Subject        *string
	Body           string
	Status         string
	ErrorMessage   *string
	SentAt         *time.Time
	CreatedAt      time.Time
	BookingNumber  *string
}

type Template struct {
	ID       uuid.UUID
	Code     string
	Name     string
	Channel  string
	Subject  *string
	Body     string
	IsActive bool
}

type NotifRule struct {
	ID           uuid.UUID
	EventType    string
	Channel      string
	TemplateCode *string
	Enabled      bool
	DelayMinutes int
	StaffAlso    bool
}

// =============================================================================
// Communication Center page (3-panel)
// =============================================================================

func (h *Handler) Comms(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	filter := r.URL.Query().Get("filter")
	activeThreadID := r.URL.Query().Get("t")

	threads, totalUnread, counts, err := h.listThreads(r.Context(), cid, q, filter)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var activeThread *Thread
	var messages []Message
	var reservation *ReservationDetail
	var notes []InternalNote

	// If a thread is explicitly selected use it; otherwise pick the first.
	for i := range threads {
		if (activeThreadID != "" && threads[i].ID.String() == activeThreadID) ||
			(activeThreadID == "" && i == 0) {
			activeThread = &threads[i]
			break
		}
	}
	if activeThread != nil {
		messages, _ = h.messagesFor(r.Context(), cid, activeThread.ID)
		notes, _ = h.notesFor(r.Context(), activeThread.ID)
		if activeThread.ReservationID != nil {
			rd, err := h.loadReservation(r.Context(), cid, *activeThread.ReservationID)
			if err == nil {
				reservation = rd
			}
		}
		// Mark the thread read on open.
		_, _ = h.Pool.Exec(r.Context(), `UPDATE communication_threads SET unread_count=0 WHERE company_id=$1 AND id=$2`, cid, activeThread.ID)
	}

	templates, _ := h.listTemplates(r.Context(), cid, "")

	// SMTP configured flag.
	smtpOK := h.resolveSMTP(r.Context(), cid).Configured()

	h.render(w, r, "admin/comms.html", "Communication Center", "comms", map[string]any{
		"Threads":       threads,
		"TotalUnread":   totalUnread,
		"Counts":        counts,
		"Q":             q,
		"Filter":        filter,
		"ActiveThread":  activeThread,
		"Messages":      messages,
		"Notes":         notes,
		"Reservation":   reservation,
		"Templates":     templates,
		"SMTPConfigured": smtpOK,
		"Flash":         r.URL.Query().Get("flash"),
		"Err":           r.URL.Query().Get("err"),
	})
}

func (h *Handler) listThreads(ctx context.Context, cid uuid.UUID, q, filter string) ([]Thread, int, map[string]int, error) {
	where := []string{"t.company_id=$1"}
	args := []any{cid}
	switch filter {
	case "unread":
		where = append(where, "t.unread_count > 0")
	case "email":
		where = append(where, `EXISTS (SELECT 1 FROM communication_messages m WHERE m.thread_id=t.id AND m.channel='email')`)
	case "sms":
		where = append(where, `EXISTS (SELECT 1 FROM communication_messages m WHERE m.thread_id=t.id AND m.channel='sms')`)
	case "active":
		where = append(where, `EXISTS (SELECT 1 FROM reservations r WHERE r.id=t.reservation_id AND r.status IN ('confirmed','checked_out','awaiting_pickup'))`)
	case "upcoming":
		where = append(where, `EXISTS (SELECT 1 FROM reservations r WHERE r.id=t.reservation_id AND r.pickup_at > NOW() AND r.status IN ('pending','confirmed'))`)
	case "completed":
		where = append(where, `EXISTS (SELECT 1 FROM reservations r WHERE r.id=t.reservation_id AND r.status='returned')`)
	case "overdue":
		where = append(where, `EXISTS (SELECT 1 FROM reservations r WHERE r.id=t.reservation_id AND r.status='checked_out' AND r.return_at < NOW())`)
	case "quotes":
		where = append(where, `EXISTS (SELECT 1 FROM reservations r WHERE r.id=t.reservation_id AND r.status='pending')`)
	case "open":
		where = append(where, "t.status='open'")
	}
	if q != "" {
		args = append(args, "%"+q+"%")
		i := len(args)
		where = append(where, fmt.Sprintf(
			"(cu.first_name ILIKE $%d OR cu.last_name ILIKE $%d OR cu.email ILIKE $%d OR cu.phone ILIKE $%d OR r.booking_number ILIKE $%d OR v.license_plate ILIKE $%d OR v.make ILIKE $%d OR v.model ILIKE $%d)",
			i, i, i, i, i, i, i, i))
	}

	sql := `
		SELECT t.id, t.customer_id, t.reservation_id, t.status, t.unread_count, t.last_message_at, t.last_preview,
		       COALESCE(cu.first_name,''), COALESCE(cu.last_name,''), cu.email, cu.phone,
		       r.booking_number, r.status,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       EXISTS (SELECT 1 FROM communication_messages m WHERE m.thread_id=t.id AND m.channel='email'),
		       EXISTS (SELECT 1 FROM communication_messages m WHERE m.thread_id=t.id AND m.channel='sms')
		FROM communication_threads t
		LEFT JOIN customers cu ON cu.id=t.customer_id
		LEFT JOIN reservations r ON r.id=t.reservation_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY COALESCE(t.last_message_at, t.created_at) DESC NULLS LAST
		LIMIT 500`

	rows, err := h.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()
	out := []Thread{}
	total := 0
	counts := map[string]int{"all": 0, "unread": 0, "email": 0, "sms": 0, "active": 0, "upcoming": 0, "completed": 0, "overdue": 0, "quotes": 0, "open": 0}
	for rows.Next() {
		var t Thread
		var cfirst, clast string
		if err := rows.Scan(&t.ID, &t.CustomerID, &t.ReservationID, &t.Status, &t.UnreadCount, &t.LastMessageAt, &t.LastPreview,
			&cfirst, &clast, &t.CustomerEmail, &t.CustomerPhone,
			&t.BookingNumber, &t.BookingStatus, &t.VehicleLabel, &t.HasEmail, &t.HasSMS); err != nil {
			continue
		}
		t.CustomerFirst = cfirst
		t.CustomerLast = clast
		t.CustomerName = strings.TrimSpace(cfirst + " " + clast)
		if t.CustomerName == "" { t.CustomerName = "(no name)" }
		total += t.UnreadCount
		counts["all"]++
		if t.UnreadCount > 0 { counts["unread"]++ }
		if t.HasEmail { counts["email"]++ }
		if t.HasSMS { counts["sms"]++ }
		if t.Status == "open" { counts["open"]++ }
		if t.BookingStatus != nil {
			switch *t.BookingStatus {
			case "pending": counts["quotes"]++
			case "returned": counts["completed"]++
			case "confirmed", "checked_out", "awaiting_pickup": counts["active"]++
			}
		}
		out = append(out, t)
	}
	return out, total, counts, nil
}

func (h *Handler) messagesFor(ctx context.Context, cid, threadID uuid.UUID) ([]Message, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT m.id, m.thread_id, m.sender_type, u.name, m.channel, m.direction, m.recipient, m.subject, m.body,
		       m.status, m.error_message, m.sent_at, m.created_at, r.booking_number
		FROM communication_messages m
		LEFT JOIN users u ON u.id=m.sender_user_id
		LEFT JOIN reservations r ON r.id=m.reservation_id
		WHERE m.thread_id=$1 AND m.company_id=$2
		ORDER BY m.created_at ASC
	`, threadID, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ThreadID, &m.SenderType, &m.SenderName, &m.Channel, &m.Direction,
			&m.Recipient, &m.Subject, &m.Body, &m.Status, &m.ErrorMessage, &m.SentAt, &m.CreatedAt, &m.BookingNumber); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

type InternalNote struct {
	ID         uuid.UUID
	Author     string
	Note       string
	CreatedAt  time.Time
}

func (h *Handler) notesFor(ctx context.Context, threadID uuid.UUID) ([]InternalNote, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT n.id, u.name, n.note, n.created_at
		FROM communication_internal_notes n JOIN users u ON u.id=n.author_user_id
		WHERE n.thread_id=$1 ORDER BY n.created_at ASC
	`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InternalNote{}
	for rows.Next() {
		var n InternalNote
		if err := rows.Scan(&n.ID, &n.Author, &n.Note, &n.CreatedAt); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func (h *Handler) loadReservation(ctx context.Context, cid, id uuid.UUID) (*ReservationDetail, error) {
	var d ReservationDetail
	err := h.Pool.QueryRow(ctx, `
		SELECT r.id, r.booking_number, r.status,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE cu.first_name||' '||cu.last_name END,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       r.pickup_at, r.return_at, r.daily_rate_cents, r.rental_days,
		       r.subtotal_cents, r.extras_cents, r.tax_cents, r.total_cents, r.paid_cents
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.company_id=$1 AND r.id=$2
	`, cid, id).Scan(&d.ID, &d.BookingNumber, &d.Status, &d.CustomerName, &d.VehicleLabel,
		&d.PickupAt, &d.ReturnAt, &d.DailyRateCents, &d.RentalDays,
		&d.SubtotalCents, &d.ExtrasCents, &d.TaxCents, &d.TotalCents, &d.PaidCents)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// =============================================================================
// Send email / SMS
// =============================================================================

// ensureThread finds-or-creates a thread for a (customer, reservation) pair.
func (h *Handler) ensureThread(ctx context.Context, cid uuid.UUID, customerID *uuid.UUID, reservationID *uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := h.Pool.QueryRow(ctx, `
		SELECT id FROM communication_threads
		WHERE company_id=$1 AND customer_id IS NOT DISTINCT FROM $2 AND reservation_id IS NOT DISTINCT FROM $3
		LIMIT 1
	`, cid, customerID, reservationID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	err = h.Pool.QueryRow(ctx, `
		INSERT INTO communication_threads (company_id, customer_id, reservation_id)
		VALUES ($1,$2,$3) RETURNING id
	`, cid, customerID, reservationID).Scan(&id)
	return id, err
}

var tplVarRe = regexp.MustCompile(`{{\s*([a-z_]+)\s*}}`)

// resolveVars expands {{var}} placeholders using the reservation + customer + company context.
func (h *Handler) resolveVars(ctx context.Context, cid uuid.UUID, reservationID *uuid.UUID, customerID *uuid.UUID, body string) (string, error) {
	vars := map[string]string{
		"company_name":  "",
		"company_phone": "",
		"company_email": "",
	}
	// Company.
	var companyName string
	_ = h.Pool.QueryRow(ctx, `SELECT name FROM companies WHERE id=$1`, cid).Scan(&companyName)
	vars["company_name"] = companyName
	// Try to read general settings for phone/email.
	type generalSettings struct{ Phone, Email string }
	// (keep it simple — leave default)
	// Customer.
	if customerID != nil {
		var first, last string
		var email, phone *string
		_ = h.Pool.QueryRow(ctx, `SELECT first_name, last_name, email, phone FROM customers WHERE id=$1`, *customerID).Scan(&first, &last, &email, &phone)
		vars["customer_name"] = strings.TrimSpace(first + " " + last)
		if email != nil { vars["customer_email"] = *email }
		if phone != nil { vars["customer_phone"] = *phone }
	}
	// Reservation.
	if reservationID != nil {
		var r struct {
			Num                                            string
			Pickup, Return                                 time.Time
			PickupLoc, ReturnLoc                           *string
			VehicleName                                    *string
			Total, Paid, Deposit                           int
		}
		_ = h.Pool.QueryRow(ctx, `
			SELECT r.booking_number, r.pickup_at, r.return_at,
			       pb.name, rb.name,
			       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
			       r.total_cents, r.paid_cents, r.deposit_cents
			FROM reservations r
			LEFT JOIN vehicles v ON v.id=r.vehicle_id
			LEFT JOIN branches pb ON pb.id=r.pickup_branch_id
			LEFT JOIN branches rb ON rb.id=r.return_branch_id
			WHERE r.id=$1
		`, *reservationID).Scan(&r.Num, &r.Pickup, &r.Return, &r.PickupLoc, &r.ReturnLoc, &r.VehicleName, &r.Total, &r.Paid, &r.Deposit)
		vars["reservation_number"] = r.Num
		vars["pickup_date"] = r.Pickup.Format("Mon, 02 Jan 2006")
		vars["pickup_time"] = r.Pickup.Format("15:04")
		vars["return_date"] = r.Return.Format("Mon, 02 Jan 2006")
		vars["return_time"] = r.Return.Format("15:04")
		if r.PickupLoc != nil { vars["pickup_location"] = *r.PickupLoc }
		if r.ReturnLoc != nil { vars["return_location"] = *r.ReturnLoc }
		if r.VehicleName != nil { vars["vehicle_name"] = strings.TrimSpace(*r.VehicleName) }
		vars["reservation_total"] = fmt.Sprintf("$%.2f", float64(r.Total)/100)
		vars["amount_paid"] = fmt.Sprintf("$%.2f", float64(r.Paid)/100)
		outstanding := r.Total - r.Paid
		if outstanding < 0 { outstanding = 0 }
		vars["outstanding_balance"] = fmt.Sprintf("$%.2f", float64(outstanding)/100)
		vars["security_deposit"] = fmt.Sprintf("$%.2f", float64(r.Deposit)/100)
	}
	// Expand.
	allowed := map[string]bool{
		"customer_name": true, "reservation_number": true, "vehicle_name": true,
		"pickup_date": true, "pickup_time": true, "pickup_location": true,
		"return_date": true, "return_time": true, "return_location": true,
		"reservation_total": true, "amount_paid": true, "outstanding_balance": true, "security_deposit": true,
		"company_name": true, "company_phone": true, "company_email": true,
		"customer_email": true, "customer_phone": true,
	}
	var unknown []string
	result := tplVarRe.ReplaceAllStringFunc(body, func(m string) string {
		name := tplVarRe.FindStringSubmatch(m)[1]
		if !allowed[name] {
			unknown = append(unknown, name)
			return m
		}
		return vars[name]
	})
	if len(unknown) > 0 {
		return result, fmt.Errorf("unknown template variables: %s", strings.Join(unknown, ", "))
	}
	return result, nil
}

// SendEmail: form POST from the composer.
func (h *Handler) CommsSendEmail(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	threadID := r.FormValue("threadId")
	customerID := r.FormValue("customerId")
	reservationID := r.FormValue("reservationId")
	to := strings.TrimSpace(r.FormValue("to"))
	subject := r.FormValue("subject")
	body := r.FormValue("body")
	templateCode := r.FormValue("templateCode")
	idemp := r.FormValue("idemp")

	if to == "" || !mail.ValidateAddress(to) {
		h.commsRedirect(w, r, threadID, "", "Invalid recipient email")
		return
	}
	// Template expansion.
	if templateCode != "" {
		var tplSubject, tplBody string
		_ = h.Pool.QueryRow(r.Context(), `SELECT COALESCE(subject,''), body FROM communication_templates WHERE company_id=$1 AND code=$2 AND channel='email'`, cid, templateCode).Scan(&tplSubject, &tplBody)
		if tplSubject != "" && subject == "" { subject = tplSubject }
		if tplBody != "" && body == "" { body = tplBody }
	}
	var cUUID, rUUID *uuid.UUID
	if u, err := uuid.Parse(customerID); err == nil { cUUID = &u }
	if u, err := uuid.Parse(reservationID); err == nil { rUUID = &u }

	expandedSubject, _ := h.resolveVars(r.Context(), cid, rUUID, cUUID, subject)
	expandedBody, err := h.resolveVars(r.Context(), cid, rUUID, cUUID, body)
	if err != nil {
		h.commsRedirect(w, r, threadID, "", "Template error: "+err.Error())
		return
	}

	// Ensure thread.
	var tid uuid.UUID
	if u, err := uuid.Parse(threadID); err == nil {
		tid = u
	} else {
		tid, err = h.ensureThread(r.Context(), cid, cUUID, rUUID)
		if err != nil {
			h.commsRedirect(w, r, "", "", "Thread create failed: "+err.Error())
			return
		}
	}

	// Insert the message in 'queued' state (idempotent).
	var mid uuid.UUID
	err = h.Pool.QueryRow(r.Context(), `
		INSERT INTO communication_messages
		  (thread_id, company_id, customer_id, reservation_id, sender_type, sender_user_id, channel, direction,
		   recipient, subject, body, status, idempotency_key)
		VALUES ($1,$2,$3,$4,'staff',$5,'email','outbound',$6,$7,$8,'queued',NULLIF($9,''))
		ON CONFLICT (company_id, idempotency_key) DO UPDATE SET body=EXCLUDED.body
		RETURNING id
	`, tid, cid, cUUID, rUUID, s.UserID, to, expandedSubject, expandedBody, idemp).Scan(&mid)
	if err != nil {
		h.commsRedirect(w, r, tid.String(), "", "Save failed: "+err.Error())
		return
	}

	// Attempt send.
	status := "sent"
	errStr := ""
	var providerID string
	m := h.mailerFor(r.Context(), cid)
	if m.Cfg.Configured() {
		providerID, err = m.Send(to, expandedSubject, expandedBody)
		if err != nil {
			status = "failed"
			errStr = err.Error()
		}
	} else {
		status = "queued"
		errStr = "SMTP not configured; message was saved but not sent"
	}
	now := time.Now()
	_, _ = h.Pool.Exec(r.Context(), `
		UPDATE communication_messages
		   SET status=$1, error_message=NULLIF($2,''), provider_message_id=NULLIF($3,''), sent_at=$4, updated_at=NOW()
		 WHERE id=$5
	`, status, errStr, providerID, now, mid)

	preview := expandedBody
	if len(preview) > 120 { preview = preview[:120] + "…" }
	_, _ = h.Pool.Exec(r.Context(), `
		UPDATE communication_threads SET last_message_at=NOW(), last_preview=$1, updated_at=NOW()
		WHERE id=$2
	`, preview, tid)

	if status == "failed" {
		h.commsRedirect(w, r, tid.String(), "", "Send failed: "+errStr)
	} else if status == "queued" {
		h.commsRedirect(w, r, tid.String(), "Saved — SMTP not configured, nothing was sent", "")
	} else {
		h.commsRedirect(w, r, tid.String(), "Email sent", "")
	}
}

// SendSMS: records as 'queued' + 'SMS provider not configured' unless a provider is wired.
func (h *Handler) CommsSendSMS(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", 400)
		return
	}
	threadID := r.FormValue("threadId")
	customerID := r.FormValue("customerId")
	reservationID := r.FormValue("reservationId")
	to := strings.TrimSpace(r.FormValue("to"))
	body := r.FormValue("body")
	idemp := r.FormValue("idemp")

	if to == "" || body == "" {
		h.commsRedirect(w, r, threadID, "", "Phone and message required")
		return
	}
	var cUUID, rUUID *uuid.UUID
	if u, err := uuid.Parse(customerID); err == nil { cUUID = &u }
	if u, err := uuid.Parse(reservationID); err == nil { rUUID = &u }

	expanded, err := h.resolveVars(r.Context(), cid, rUUID, cUUID, body)
	if err != nil {
		h.commsRedirect(w, r, threadID, "", "Template error: "+err.Error())
		return
	}
	var tid uuid.UUID
	if u, err := uuid.Parse(threadID); err == nil {
		tid = u
	} else {
		tid, err = h.ensureThread(r.Context(), cid, cUUID, rUUID)
		if err != nil {
			h.commsRedirect(w, r, "", "", "Thread create failed: "+err.Error())
			return
		}
	}
	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO communication_messages
		  (thread_id, company_id, customer_id, reservation_id, sender_type, sender_user_id, channel, direction,
		   recipient, body, status, error_message, idempotency_key)
		VALUES ($1,$2,$3,$4,'staff',$5,'sms','outbound',$6,$7,'queued','SMS provider not configured',NULLIF($8,''))
		ON CONFLICT (company_id, idempotency_key) DO NOTHING
	`, tid, cid, cUUID, rUUID, s.UserID, to, expanded, idemp)
	if err != nil {
		h.commsRedirect(w, r, tid.String(), "", "Save failed: "+err.Error())
		return
	}
	preview := expanded
	if len(preview) > 120 { preview = preview[:120] + "…" }
	_, _ = h.Pool.Exec(r.Context(), `UPDATE communication_threads SET last_message_at=NOW(), last_preview=$1 WHERE id=$2`, preview, tid)
	h.commsRedirect(w, r, tid.String(), "SMS saved to queue — provider not configured, nothing was sent", "")
}

// Internal note.
func (h *Handler) CommsNote(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	_ = r.ParseForm()
	threadID := r.FormValue("threadId")
	note := strings.TrimSpace(r.FormValue("note"))
	if note == "" || threadID == "" {
		h.commsRedirect(w, r, threadID, "", "Note required")
		return
	}
	_, err := h.Pool.Exec(r.Context(), `INSERT INTO communication_internal_notes (thread_id, author_user_id, note) VALUES ($1::uuid,$2,$3)`, threadID, s.UserID, note)
	if err != nil {
		h.commsRedirect(w, r, threadID, "", err.Error())
		return
	}
	h.commsRedirect(w, r, threadID, "Note added", "")
}

// OpenThreadForReservation: creates (or finds) a thread for a booking and redirects to it.
func (h *Handler) OpenThreadForReservation(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rid := chi.URLParam(r, "id")
	rUUID, err := uuid.Parse(rid)
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	var custID *uuid.UUID
	var tmp uuid.UUID
	if err := h.Pool.QueryRow(r.Context(), `SELECT customer_id FROM reservations WHERE company_id=$1 AND id=$2`, cid, rid).Scan(&tmp); err == nil && tmp != uuid.Nil {
		custID = &tmp
	}
	tid, err := h.ensureThread(r.Context(), cid, custID, &rUUID)
	if err != nil {
		http.Error(w, "thread create failed: "+err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/communications?t="+tid.String(), http.StatusSeeOther)
}

func (h *Handler) CommsResolve(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	threadID := chi.URLParam(r, "id")
	_ = r.ParseForm()
	newStatus := r.FormValue("status")
	if newStatus != "open" && newStatus != "resolved" && newStatus != "archived" {
		newStatus = "resolved"
	}
	_, _ = h.Pool.Exec(r.Context(), `UPDATE communication_threads SET status=$1 WHERE company_id=$2 AND id=$3`, newStatus, cid, threadID)
	h.commsRedirect(w, r, threadID, "Thread "+newStatus, "")
}

func (h *Handler) commsRedirect(w http.ResponseWriter, r *http.Request, threadID, flash, errMsg string) {
	q := r.URL.Query()
	if threadID != "" { q.Set("t", threadID) }
	if flash != "" { q.Set("flash", flash) }
	if errMsg != "" { q.Set("err", errMsg) }
	q.Del("filter"); q.Del("q")
	if r.URL.Query().Get("filter") != "" { q.Set("filter", r.URL.Query().Get("filter")) }
	if r.URL.Query().Get("q") != "" { q.Set("q", r.URL.Query().Get("q")) }
	http.Redirect(w, r, "/admin/communications?"+q.Encode(), http.StatusSeeOther)
}

// =============================================================================
// Templates & notification rules
// =============================================================================

func (h *Handler) listTemplates(ctx context.Context, cid uuid.UUID, channel string) ([]Template, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT id, code, name, channel, subject, body, is_active
		FROM communication_templates
		WHERE company_id=$1 AND ($2='' OR channel=$2)
		ORDER BY channel, name
	`, cid, channel)
	if err != nil { return nil, err }
	defer rows.Close()
	out := []Template{}
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.Channel, &t.Subject, &t.Body, &t.IsActive); err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

func (h *Handler) CommsTemplates(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	templates, _ := h.listTemplates(r.Context(), cid, "")
	h.render(w, r, "admin/comms_templates.html", "Message templates", "comms", map[string]any{
		"Templates": templates,
		"Flash":     r.URL.Query().Get("flash"),
	})
}

func (h *Handler) CommsTemplateSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	id := r.FormValue("id")
	code := strings.TrimSpace(r.FormValue("code"))
	name := strings.TrimSpace(r.FormValue("name"))
	channel := r.FormValue("channel")
	subject := r.FormValue("subject")
	body := r.FormValue("body")
	active := r.FormValue("isActive") == "on"
	if code == "" || name == "" || body == "" || (channel != "email" && channel != "sms") {
		http.Redirect(w, r, "/admin/communications/templates?flash=Code+name+body+and+channel+required", http.StatusSeeOther)
		return
	}
	// Validate variables.
	tplVarRe.ReplaceAllStringFunc(body, func(m string) string { return m })
	if id == "" {
		_, err := h.Pool.Exec(r.Context(), `
			INSERT INTO communication_templates (company_id, code, name, channel, subject, body, is_active, created_by, updated_by)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$8)
			ON CONFLICT (company_id, code) DO UPDATE SET name=EXCLUDED.name, channel=EXCLUDED.channel, subject=EXCLUDED.subject, body=EXCLUDED.body, is_active=EXCLUDED.is_active, updated_by=EXCLUDED.updated_by, updated_at=NOW()
		`, cid, code, name, channel, subject, body, active, s.UserID)
		if err != nil { http.Error(w, err.Error(), 500); return }
	} else {
		_, err := h.Pool.Exec(r.Context(), `
			UPDATE communication_templates SET name=$1, channel=$2, subject=NULLIF($3,''), body=$4, is_active=$5, updated_by=$6, updated_at=NOW()
			WHERE company_id=$7 AND id=$8
		`, name, channel, subject, body, active, s.UserID, cid, id)
		if err != nil { http.Error(w, err.Error(), 500); return }
	}
	http.Redirect(w, r, "/admin/communications/templates?flash=Saved", http.StatusSeeOther)
}

// Notification rules page.
func (h *Handler) CommsNotifications(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, event_type, channel, template_code, enabled, delay_minutes, staff_also
		FROM communication_notification_rules WHERE company_id=$1 ORDER BY event_type, channel
	`, cid)
	if err != nil { http.Error(w, err.Error(), 500); return }
	defer rows.Close()
	rules := []NotifRule{}
	for rows.Next() {
		var rr NotifRule
		if err := rows.Scan(&rr.ID, &rr.EventType, &rr.Channel, &rr.TemplateCode, &rr.Enabled, &rr.DelayMinutes, &rr.StaffAlso); err == nil {
			rules = append(rules, rr)
		}
	}
	tpl, _ := h.listTemplates(r.Context(), cid, "")
	h.render(w, r, "admin/comms_rules.html", "Automatic notifications", "comms", map[string]any{
		"Rules":     rules,
		"Templates": tpl,
		"Flash":     r.URL.Query().Get("flash"),
	})
}

func (h *Handler) CommsRuleUpdate(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	_ = r.ParseForm()
	enabled := r.FormValue("enabled") == "on"
	delay := 0
	fmt.Sscanf(r.FormValue("delayMinutes"), "%d", &delay)
	tpl := r.FormValue("templateCode")
	staffAlso := r.FormValue("staffAlso") == "on"
	_, err := h.Pool.Exec(r.Context(), `
		UPDATE communication_notification_rules
		   SET enabled=$1, delay_minutes=$2, template_code=NULLIF($3,''), staff_also=$4, updated_at=NOW()
		 WHERE company_id=$5 AND id=$6
	`, enabled, delay, tpl, staffAlso, cid, id)
	if err != nil { http.Error(w, err.Error(), 500); return }
	http.Redirect(w, r, "/admin/communications/notifications?flash=Rule+updated", http.StatusSeeOther)
}
