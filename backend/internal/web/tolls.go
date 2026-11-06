package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// =============================================================================
// TOLL MANAGEMENT
//
// Scope of this module:
//   - manage E-ZPass (and compatible) transponders and their assignment
//     history against vehicles
//   - import toll transactions from a provider CSV export (E-ZPass Virginia's
//     Business portal export is the primary format supported)
//   - match each imported transaction to the rental reservation that was
//     active for that vehicle at the transaction timestamp, so the customer
//     who was driving gets billed and not somebody else
//   - give the manager a transactions screen with reassign / verify / exclude
//
// Deferred to follow-up turns:
//   - supplemental toll invoices (ties into the existing invoice module)
//   - Stripe payment links for toll invoices
//   - toll-specific reports (we reuse the existing report bundle for now)
// =============================================================================

// =============================================================================
// Domain types
// =============================================================================

type TollAccount struct {
	ID                uuid.UUID
	Provider          string
	AccountReference  *string
	ImportMethod      string
	ConnectionStatus  string
	LastSyncAt        *time.Time
	LastSyncError     *string
	AdminFeeCents     int
}

type Transponder struct {
	ID            uuid.UUID
	TransponderID string
	SerialNumber  *string
	Status        string
	Notes         *string
	VehicleID     *uuid.UUID
	VehicleLabel  *string
	AssignedAt    *time.Time
}

type TollTx struct {
	ID               uuid.UUID
	ProviderTxID     string
	Provider         string
	TransponderRef   *string
	TransponderLabel string
	VehicleLabel     string
	ReservationNo    string
	CustomerName     string
	LicensePlate     *string
	Location         *string
	EntryPlaza       *string
	ExitPlaza        *string
	TransactionAt    time.Time
	AmountCents      int
	AdminFeeCents    int
	TotalCents       int
	MatchStatus      string
	BillingStatus    string
	ReservationID    *uuid.UUID
	VehicleID        *uuid.UUID
}

func (t TollTx) TotalFor() int { return t.AmountCents + t.AdminFeeCents }

// =============================================================================
// Account + settings
// =============================================================================

func (h *Handler) tollAccount(r *http.Request, cid uuid.UUID) *TollAccount {
	acc := &TollAccount{}
	err := h.Pool.QueryRow(r.Context(), `
		SELECT id, provider, account_reference, import_method, connection_status,
		       last_sync_at, last_sync_error, admin_fee_cents
		FROM toll_accounts WHERE company_id=$1
	`, cid).Scan(&acc.ID, &acc.Provider, &acc.AccountReference, &acc.ImportMethod,
		&acc.ConnectionStatus, &acc.LastSyncAt, &acc.LastSyncError, &acc.AdminFeeCents)
	if err != nil {
		return nil
	}
	return acc
}

func (h *Handler) ensureTollAccount(r *http.Request, cid uuid.UUID) (*TollAccount, error) {
	acc := h.tollAccount(r, cid)
	if acc != nil {
		return acc, nil
	}
	_, err := h.Pool.Exec(r.Context(),
		`INSERT INTO toll_accounts (company_id, provider) VALUES ($1,'ezpass_va') ON CONFLICT DO NOTHING`, cid)
	if err != nil {
		return nil, err
	}
	return h.tollAccount(r, cid), nil
}

// TollSettings is the account page under /admin/tolls/account.
func (h *Handler) TollSettings(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	acc, err := h.ensureTollAccount(r, cid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Recent imports.
	rows, _ := h.Pool.Query(r.Context(), `
		SELECT provider, source_filename, rows_total, rows_inserted, rows_duplicate, rows_error, created_at
		FROM toll_imports WHERE company_id=$1 ORDER BY created_at DESC LIMIT 20
	`, cid)
	type imp struct {
		Provider, Filename               string
		Total, Inserted, Dup, ErrorCount int
		CreatedAt                        time.Time
	}
	imports := []imp{}
	if rows != nil {
		for rows.Next() {
			var i imp
			var fn *string
			if err := rows.Scan(&i.Provider, &fn, &i.Total, &i.Inserted, &i.Dup, &i.ErrorCount, &i.CreatedAt); err == nil {
				if fn != nil {
					i.Filename = *fn
				}
				imports = append(imports, i)
			}
		}
		rows.Close()
	}
	h.render(w, r, "admin/tolls_account.html", "Toll account", "tolls", map[string]any{
		"Account": acc,
		"Imports": imports,
		"Flash":   r.URL.Query().Get("flash"),
		"Err":     r.URL.Query().Get("err"),
	})
}

// TollSettingsSave updates the admin fee and account reference.
func (h *Handler) TollSettingsSave(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_, _ = h.ensureTollAccount(r, cid)
	_ = r.ParseForm()
	ref := strings.TrimSpace(r.FormValue("accountReference"))
	fee := parseDollarsOrCents(r.FormValue("adminFeeDollars"), r.FormValue("adminFeeCents"))
	if fee < 0 {
		fee = 0
	}
	_, err := h.Pool.Exec(r.Context(), `
		UPDATE toll_accounts SET account_reference=NULLIF($1,''), admin_fee_cents=$2,
		       connection_status=CASE WHEN COALESCE(account_reference,'')<>'' OR $1<>'' THEN 'ready' ELSE connection_status END,
		       updated_at=NOW()
		WHERE company_id=$3
	`, ref, fee, cid)
	if err != nil {
		http.Redirect(w, r, "/admin/tolls/account?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/tolls/account?flash=Saved", http.StatusSeeOther)
}

// =============================================================================
// Transponder management
// =============================================================================

func (h *Handler) TransponderList(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rows, err := h.Pool.Query(r.Context(), `
		SELECT t.id, t.transponder_id, t.serial_number, t.status, t.notes,
		       va.vehicle_id, va.assigned_at,
		       CASE WHEN v.id IS NULL THEN NULL
		            ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'')||
		                 CASE WHEN v.license_plate IS NULL THEN '' ELSE ' ('||v.license_plate||')' END
		       END AS vehicle_label
		FROM toll_transponders t
		LEFT JOIN vehicle_transponder_assignments va
		  ON va.transponder_id=t.id AND va.removed_at IS NULL
		LEFT JOIN vehicles v ON v.id=va.vehicle_id
		WHERE t.company_id=$1
		ORDER BY t.transponder_id
	`, cid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()
	transponders := []Transponder{}
	for rows.Next() {
		var t Transponder
		if err := rows.Scan(&t.ID, &t.TransponderID, &t.SerialNumber, &t.Status, &t.Notes,
			&t.VehicleID, &t.AssignedAt, &t.VehicleLabel); err == nil {
			transponders = append(transponders, t)
		}
	}
	// List of vehicles for the assign dropdown.
	type veh struct {
		ID    uuid.UUID
		Label string
	}
	vehicles := []veh{}
	vrows, _ := h.Pool.Query(r.Context(), `
		SELECT id, COALESCE(year::text,'')||' '||COALESCE(make,'')||' '||COALESCE(model,'')||
		       CASE WHEN license_plate IS NULL THEN '' ELSE ' ('||license_plate||')' END
		FROM vehicles WHERE company_id=$1 ORDER BY make, model`, cid)
	if vrows != nil {
		for vrows.Next() {
			var v veh
			if err := vrows.Scan(&v.ID, &v.Label); err == nil {
				vehicles = append(vehicles, v)
			}
		}
		vrows.Close()
	}
	h.render(w, r, "admin/transponders.html", "Transponders", "tolls", map[string]any{
		"Transponders": transponders,
		"Vehicles":     vehicles,
		"Flash":        r.URL.Query().Get("flash"),
		"Err":          r.URL.Query().Get("err"),
	})
}

// TransponderCreate adds a new transponder (optionally assigning on creation).
func (h *Handler) TransponderCreate(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	acc, _ := h.ensureTollAccount(r, cid)
	_ = r.ParseForm()
	tagID := strings.TrimSpace(r.FormValue("transponderId"))
	serial := strings.TrimSpace(r.FormValue("serialNumber"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	vehicleID := strings.TrimSpace(r.FormValue("vehicleId"))
	if tagID == "" {
		http.Redirect(w, r, "/admin/transponders?err=Tag+number+required", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback(ctx)
	var tid uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO toll_transponders (company_id, toll_account_id, transponder_id, serial_number, notes)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''))
		RETURNING id
	`, cid, nilIfZero(acc), tagID, serial, notes).Scan(&tid)
	if err != nil {
		http.Redirect(w, r, "/admin/transponders?err=create%3A+"+err.Error(), http.StatusSeeOther)
		return
	}
	if vehicleID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO vehicle_transponder_assignments (company_id, transponder_id, vehicle_id, created_by)
			VALUES ($1,$2,$3::uuid,$4)
		`, cid, tid, vehicleID, s.UserID); err != nil {
			http.Redirect(w, r, "/admin/transponders?err=assign%3A+"+err.Error(), http.StatusSeeOther)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		http.Redirect(w, r, "/admin/transponders?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/transponders?flash=Transponder+added", http.StatusSeeOther)
}

// TransponderAssign reassigns a transponder to a different vehicle, closing
// the previous assignment (preserves history).
func (h *Handler) TransponderAssign(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	tid := chi.URLParam(r, "id")
	_ = r.ParseForm()
	vehicleID := strings.TrimSpace(r.FormValue("vehicleId"))
	ctx := r.Context()
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback(ctx)
	// Close the current assignment (if any).
	_, _ = tx.Exec(ctx, `
		UPDATE vehicle_transponder_assignments
		   SET removed_at=NOW()
		 WHERE transponder_id=$1 AND removed_at IS NULL
	`, tid)
	if vehicleID != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO vehicle_transponder_assignments (company_id, transponder_id, vehicle_id, created_by)
			VALUES ($1,$2::uuid,$3::uuid,$4)
		`, cid, tid, vehicleID, s.UserID); err != nil {
			http.Redirect(w, r, "/admin/transponders?err=assign%3A+"+err.Error(), http.StatusSeeOther)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		http.Redirect(w, r, "/admin/transponders?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/transponders?flash=Assignment+updated", http.StatusSeeOther)
}

// TransponderStatus flips active/inactive/lost/damaged.
func (h *Handler) TransponderStatus(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	tid := chi.URLParam(r, "id")
	_ = r.ParseForm()
	status := r.FormValue("status")
	ok := map[string]bool{"active": true, "inactive": true, "lost": true, "damaged": true, "under_replacement": true}
	if !ok[status] {
		http.Redirect(w, r, "/admin/transponders?err=Invalid+status", http.StatusSeeOther)
		return
	}
	_, err := h.Pool.Exec(r.Context(),
		`UPDATE toll_transponders SET status=$1, updated_at=NOW() WHERE company_id=$2 AND id=$3`,
		status, cid, tid)
	if err != nil {
		http.Redirect(w, r, "/admin/transponders?err="+err.Error(), http.StatusSeeOther)
		return
	}
	// If marking lost/damaged/inactive, close the current assignment.
	if status != "active" {
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE vehicle_transponder_assignments SET removed_at=NOW()
			 WHERE transponder_id=$1 AND removed_at IS NULL`, tid)
	}
	http.Redirect(w, r, "/admin/transponders?flash=Status+updated", http.StatusSeeOther)
}

// =============================================================================
// Toll transactions UI
// =============================================================================

// TollDashboard: KPIs + filter form + recent transactions.
func (h *Handler) TollDashboard(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_, _ = h.ensureTollAccount(r, cid)

	// KPIs
	kpi := map[string]int{}
	type kv struct {
		Name, SQL string
	}
	queries := []kv{
		{"total_tx", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1`},
		{"total_amount", `SELECT COALESCE(SUM(amount_cents+admin_fee_cents),0) FROM toll_transactions WHERE company_id=$1`},
		{"unmatched", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1 AND match_status IN ('pending','needs_review')`},
		{"unbilled", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1 AND match_status='matched' AND billing_status='unbilled'`},
		{"unbilled_amount", `SELECT COALESCE(SUM(amount_cents+admin_fee_cents),0) FROM toll_transactions WHERE company_id=$1 AND match_status='matched' AND billing_status='unbilled'`},
		{"invoiced", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1 AND billing_status='invoiced'`},
		{"paid_count", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1 AND billing_status='paid'`},
		{"company_expense", `SELECT COUNT(*) FROM toll_transactions WHERE company_id=$1 AND match_status='company_expense'`},
	}
	for _, q := range queries {
		var n int
		_ = h.Pool.QueryRow(r.Context(), q.SQL, cid).Scan(&n)
		kpi[q.Name] = n
	}

	// Recent transactions
	txs := h.loadTollTxs(r, cid, "", "", "", 25)

	h.render(w, r, "admin/tolls_dashboard.html", "Toll dashboard", "tolls", map[string]any{
		"KPI":   kpi,
		"Recent": txs,
	})
}

// TollTransactions: filterable list of all toll transactions.
func (h *Handler) TollTransactions(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	q := r.URL.Query()
	txs := h.loadTollTxs(r, cid,
		q.Get("match"), q.Get("billing"), q.Get("q"), 500)
	h.render(w, r, "admin/tolls_transactions.html", "Toll transactions", "tolls", map[string]any{
		"Transactions":    txs,
		"FilterMatch":     q.Get("match"),
		"FilterBilling":   q.Get("billing"),
		"FilterQuery":     q.Get("q"),
		"Flash":           r.URL.Query().Get("flash"),
		"Err":             r.URL.Query().Get("err"),
	})
}

func (h *Handler) loadTollTxs(r *http.Request, cid uuid.UUID, match, billing, search string, limit int) []TollTx {
	where := []string{"tt.company_id=$1"}
	args := []any{cid}
	if match != "" {
		args = append(args, match)
		where = append(where, fmt.Sprintf("tt.match_status=$%d", len(args)))
	}
	if billing != "" {
		args = append(args, billing)
		where = append(where, fmt.Sprintf("tt.billing_status=$%d", len(args)))
	}
	if search != "" {
		args = append(args, "%"+strings.ToLower(search)+"%")
		idx := len(args)
		where = append(where, fmt.Sprintf(
			"(LOWER(COALESCE(tt.transponder_ref,'')) LIKE $%d OR LOWER(COALESCE(tt.license_plate,'')) LIKE $%d OR LOWER(COALESCE(tt.toll_location,'')) LIKE $%d OR LOWER(COALESCE(r.booking_number,'')) LIKE $%d)",
			idx, idx, idx, idx))
	}
	args = append(args, limit)
	sql := `
		SELECT tt.id, tt.provider_transaction_id, tt.provider, tt.transponder_ref,
		       COALESCE(tp.transponder_id, tt.transponder_ref, ''),
		       COALESCE(COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),''),
		       COALESCE(r.booking_number,''),
		       COALESCE(cu.first_name||' '||cu.last_name,''),
		       tt.license_plate, tt.toll_location, tt.entry_plaza, tt.exit_plaza,
		       tt.transaction_at, tt.amount_cents, tt.admin_fee_cents,
		       tt.match_status, tt.billing_status,
		       tt.reservation_id, tt.vehicle_id
		FROM toll_transactions tt
		LEFT JOIN toll_transponders tp ON tp.id=tt.transponder_id
		LEFT JOIN vehicles v ON v.id=tt.vehicle_id
		LEFT JOIN reservations r ON r.id=tt.reservation_id
		LEFT JOIN customers cu ON cu.id=tt.customer_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY tt.transaction_at DESC
		LIMIT $` + strconv.Itoa(len(args))
	rows, err := h.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []TollTx{}
	for rows.Next() {
		var t TollTx
		if err := rows.Scan(&t.ID, &t.ProviderTxID, &t.Provider, &t.TransponderRef,
			&t.TransponderLabel, &t.VehicleLabel, &t.ReservationNo, &t.CustomerName,
			&t.LicensePlate, &t.Location, &t.EntryPlaza, &t.ExitPlaza,
			&t.TransactionAt, &t.AmountCents, &t.AdminFeeCents,
			&t.MatchStatus, &t.BillingStatus, &t.ReservationID, &t.VehicleID); err != nil {
			continue
		}
		t.TotalCents = t.AmountCents + t.AdminFeeCents
		out = append(out, t)
	}
	return out
}

// TollTxAction performs one of: verify, exclude, needs-review, reset, rematch.
func (h *Handler) TollTxAction(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	_ = r.ParseForm()
	reason := strings.TrimSpace(r.FormValue("reason"))

	switch action {
	case "verify":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE toll_transactions SET verified_at=NOW(), verified_by=$1, updated_at=NOW()
			 WHERE company_id=$2 AND id=$3`, s.UserID, cid, id)
	case "exclude":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE toll_transactions
			   SET match_status='excluded', exclusion_reason=NULLIF($1,''), updated_at=NOW()
			 WHERE company_id=$2 AND id=$3`, reason, cid, id)
	case "review":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE toll_transactions SET match_status='needs_review', updated_at=NOW()
			 WHERE company_id=$1 AND id=$2`, cid, id)
	case "company-expense":
		_, _ = h.Pool.Exec(r.Context(),
			`UPDATE toll_transactions SET match_status='company_expense', reservation_id=NULL,
			       customer_id=NULL, updated_at=NOW()
			 WHERE company_id=$1 AND id=$2`, cid, id)
	case "rematch":
		if rid, vid, cuid, status, err := h.matchOne(r, cid, id); err == nil {
			_, _ = h.Pool.Exec(r.Context(),
				`UPDATE toll_transactions SET reservation_id=$1, vehicle_id=$2, customer_id=$3,
				 match_status=$4, updated_at=NOW()
				 WHERE company_id=$5 AND id=$6`,
				rid, vid, cuid, status, cid, id)
		}
	case "reassign":
		rid := strings.TrimSpace(r.FormValue("reservationId"))
		if rid == "" {
			http.Redirect(w, r, "/admin/tolls/transactions?err=Reservation+required", http.StatusSeeOther)
			return
		}
		var vid, cuid uuid.UUID
		if err := h.Pool.QueryRow(r.Context(),
			`SELECT vehicle_id, customer_id FROM reservations WHERE company_id=$1 AND id=$2`,
			cid, rid).Scan(&vid, &cuid); err != nil {
			http.Redirect(w, r, "/admin/tolls/transactions?err=Reservation+not+found", http.StatusSeeOther)
			return
		}
		_, _ = h.Pool.Exec(r.Context(), `
			UPDATE toll_transactions SET reservation_id=$1::uuid, vehicle_id=$2, customer_id=$3,
			       match_status='matched', updated_at=NOW()
			 WHERE company_id=$4 AND id=$5`, rid, vid, cuid, cid, id)
	default:
		http.Redirect(w, r, "/admin/tolls/transactions?err=Unknown+action", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/tolls/transactions?flash=Updated", http.StatusSeeOther)
}

// =============================================================================
// CSV import — E-ZPass Virginia (and compatible) exports
// =============================================================================

// TollImportForm renders the upload page.
func (h *Handler) TollImportForm(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	acc, _ := h.ensureTollAccount(r, cid)
	h.render(w, r, "admin/tolls_import.html", "Import tolls", "tolls", map[string]any{
		"Account": acc,
		"Flash":   r.URL.Query().Get("flash"),
		"Err":     r.URL.Query().Get("err"),
	})
}

// TollImportUpload accepts a multipart CSV, parses it, upserts transactions,
// runs the matching engine, and records the import run.
func (h *Handler) TollImportUpload(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	acc, err := h.ensureTollAccount(r, cid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Redirect(w, r, "/admin/tolls/import?err=Bad+upload", http.StatusSeeOther)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Redirect(w, r, "/admin/tolls/import?err=File+required", http.StatusSeeOther)
		return
	}
	hdr := files[0]
	f, err := hdr.Open()
	if err != nil {
		http.Redirect(w, r, "/admin/tolls/import?err="+err.Error(), http.StatusSeeOther)
		return
	}
	defer f.Close()

	result, err := h.importTollCSV(r, cid, acc, f, hdr.Filename, s.UserID)
	if err != nil {
		http.Redirect(w, r, "/admin/tolls/import?err="+err.Error(), http.StatusSeeOther)
		return
	}
	msg := fmt.Sprintf("Imported+%d+new%2C+%d+duplicates%2C+%d+errors", result.Inserted, result.Duplicate, result.ErrorCount)
	http.Redirect(w, r, "/admin/tolls/account?flash="+msg, http.StatusSeeOther)
}

type importResult struct {
	Total, Inserted, Duplicate, ErrorCount int
}

// importTollCSV parses E-ZPass Virginia's Business portal export. Column
// headers vary across statement types, so we accept several aliases.
func (h *Handler) importTollCSV(r *http.Request, cid uuid.UUID, acc *TollAccount, body io.Reader, filename string, uploadedBy uuid.UUID) (importResult, error) {
	res := importResult{}
	rdr := csv.NewReader(body)
	rdr.FieldsPerRecord = -1
	rdr.TrimLeadingSpace = true

	headers, err := rdr.Read()
	if err != nil {
		return res, fmt.Errorf("read header: %w", err)
	}
	cols := map[string]int{}
	for i, h := range headers {
		cols[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, aliases ...string) string {
		for _, a := range aliases {
			if idx, ok := cols[a]; ok && idx < len(row) {
				return strings.TrimSpace(row[idx])
			}
		}
		return ""
	}

	// Error collector (short strings for the audit row).
	var errs []string

	for {
		row, err := rdr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, err.Error())
			res.ErrorCount++
			continue
		}
		res.Total++

		providerTxID := firstNonEmpty(
			get(row, "transaction id", "transaction #", "transaction number", "trans id", "txn id", "record id"),
			// E-ZPass VA sometimes doesn't include an ID — synthesize a deterministic one.
			// We hash the date+time+plaza+tag+amount so re-imports stay idempotent.
			"",
		)
		tagRef := get(row, "transponder", "tag number", "tag #", "transponder id", "tag id", "device")
		plate := get(row, "license plate", "plate", "vehicle plate")
		plaza := get(row, "plaza", "toll plaza", "location", "facility")
		entry := get(row, "entry plaza", "entry", "entry point")
		exit := get(row, "exit plaza", "exit", "exit point")
		dateStr := get(row, "date", "transaction date", "posting date")
		timeStr := get(row, "time", "transaction time")
		combined := get(row, "transaction date/time", "datetime", "transaction datetime", "posted on")
		amtStr := get(row, "amount", "toll amount", "amount ($)", "fare")

		when, perr := parseTollDateTime(combined, dateStr, timeStr)
		if perr != nil {
			errs = append(errs, "row "+strconv.Itoa(res.Total)+": "+perr.Error())
			res.ErrorCount++
			continue
		}
		amt := parseAmountCents(amtStr)

		if providerTxID == "" {
			providerTxID = fmt.Sprintf("%s|%s|%s|%d",
				when.UTC().Format("20060102T150405"), tagRef, plaza, amt)
		}

		raw, _ := json.Marshal(mapRow(headers, row))

		// Resolve the transponder to our internal id (if known).
		var tpID *uuid.UUID
		if tagRef != "" {
			var id uuid.UUID
			if err := h.Pool.QueryRow(r.Context(),
				`SELECT id FROM toll_transponders WHERE company_id=$1 AND transponder_id=$2`,
				cid, tagRef).Scan(&id); err == nil {
				tpID = &id
			}
		}

		// Insert, skip on duplicate.
		var insertedID uuid.UUID
		err = h.Pool.QueryRow(r.Context(), `
			INSERT INTO toll_transactions
			  (company_id, provider, provider_transaction_id, transponder_ref, transponder_id,
			   license_plate, toll_location, entry_plaza, exit_plaza,
			   transaction_at, amount_cents, admin_fee_cents, raw_row)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13::jsonb)
			ON CONFLICT (company_id, provider, provider_transaction_id) DO NOTHING
			RETURNING id
		`, cid, acc.Provider, providerTxID, tagRef, tpID,
			plate, plaza, entry, exit,
			when, amt, acc.AdminFeeCents, string(raw)).Scan(&insertedID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				res.Duplicate++
				continue
			}
			errs = append(errs, "row "+strconv.Itoa(res.Total)+": "+err.Error())
			res.ErrorCount++
			continue
		}
		res.Inserted++

		// Match this transaction to a reservation.
		if rid, vid, cuid, status, merr := h.matchOne(r, cid, insertedID.String()); merr == nil {
			_, _ = h.Pool.Exec(r.Context(), `
				UPDATE toll_transactions SET reservation_id=$1, vehicle_id=$2, customer_id=$3,
				       match_status=$4, updated_at=NOW()
				 WHERE id=$5`, rid, vid, cuid, status, insertedID)
		}
	}

	errText := ""
	if len(errs) > 0 {
		errText = strings.Join(errs, "\n")
		if len(errText) > 2000 {
			errText = errText[:2000] + "…(truncated)"
		}
	}

	_, _ = h.Pool.Exec(r.Context(), `
		INSERT INTO toll_imports (company_id, provider, source_filename, rows_total,
		                          rows_inserted, rows_duplicate, rows_error, error_detail, uploaded_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9)
	`, cid, acc.Provider, filename, res.Total, res.Inserted, res.Duplicate, res.ErrorCount, errText, uploadedBy)

	_, _ = h.Pool.Exec(r.Context(),
		`UPDATE toll_accounts SET last_sync_at=NOW(), last_sync_error=NULLIF($1,''), updated_at=NOW()
		 WHERE company_id=$2`, errText, cid)

	return res, nil
}

// =============================================================================
// Manual toll — used from the reservation detail page to add a late-arriving
// charge after the vehicle has already been returned.
// =============================================================================

// ReservationTollAdd inserts a toll_transactions row pre-attached to the given
// reservation. No provider file needed — this is the manager typing it in.
func (h *Handler) ReservationTollAdd(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	rid := chi.URLParam(r, "id")
	_ = r.ParseForm()

	location := strings.TrimSpace(r.FormValue("location"))
	whenStr := strings.TrimSpace(r.FormValue("transactionAt"))
	if whenStr == "" {
		whenStr = time.Now().Format("2006-01-02T15:04")
	}
	when, err := parseLocal(whenStr)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+rid+"?err=Invalid+date#tolls", http.StatusSeeOther)
		return
	}
	amount := parseDollarsOrCents(r.FormValue("amountDollars"), r.FormValue("amountCents"))
	adminFee := parseDollarsOrCents(r.FormValue("adminFeeDollars"), r.FormValue("adminFeeCents"))
	if amount <= 0 {
		http.Redirect(w, r, "/admin/reservations/"+rid+"?err=Amount+required#tolls", http.StatusSeeOther)
		return
	}

	// Look up the reservation's vehicle + customer for proper attribution.
	var vehicleID, customerID uuid.UUID
	if err := h.Pool.QueryRow(r.Context(),
		`SELECT vehicle_id, customer_id FROM reservations WHERE company_id=$1 AND id=$2`,
		cid, rid).Scan(&vehicleID, &customerID); err != nil {
		http.Redirect(w, r, "/admin/reservations/"+rid+"?err=Reservation+not+found#tolls", http.StatusSeeOther)
		return
	}

	// Fall back to the account's default admin fee if the manager left it blank.
	if adminFee == 0 {
		if acc, _ := h.ensureTollAccount(r, cid); acc != nil {
			adminFee = acc.AdminFeeCents
		}
	}

	// Synthesize a stable provider_transaction_id for the manual row so
	// re-posting an identical form doesn't create a duplicate.
	providerTxID := fmt.Sprintf("manual|%s|%d|%s", rid, amount, when.UTC().Format("20060102T150405"))

	_, err = h.Pool.Exec(r.Context(), `
		INSERT INTO toll_transactions
		  (company_id, provider, provider_transaction_id, vehicle_id, reservation_id, customer_id,
		   toll_location, transaction_at, amount_cents, admin_fee_cents, match_status, billing_status, verified_at, verified_by)
		VALUES ($1,'manual',$2,$3,$4::uuid,$5,NULLIF($6,''),$7,$8,$9,'matched','unbilled',NOW(),$10)
		ON CONFLICT (company_id, provider, provider_transaction_id) DO NOTHING
	`, cid, providerTxID, vehicleID, rid, customerID, location, when, amount, adminFee, s.UserID)
	if err != nil {
		http.Redirect(w, r, "/admin/reservations/"+rid+"?err="+err.Error()+"#tolls", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/reservations/"+rid+"?flash=Toll+added#tolls", http.StatusSeeOther)
}

// TollsForReservation returns every toll transaction tied to a reservation
// (used by both admin and customer views). Ordered oldest-first.
func (h *Handler) TollsForReservation(r *http.Request, cid, rid uuid.UUID) []TollTx {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT tt.id, tt.provider_transaction_id, tt.provider, tt.transponder_ref,
		       COALESCE(tp.transponder_id, tt.transponder_ref, ''),
		       '', '', '',
		       tt.license_plate, tt.toll_location, tt.entry_plaza, tt.exit_plaza,
		       tt.transaction_at, tt.amount_cents, tt.admin_fee_cents,
		       tt.match_status, tt.billing_status,
		       tt.reservation_id, tt.vehicle_id
		FROM toll_transactions tt
		LEFT JOIN toll_transponders tp ON tp.id=tt.transponder_id
		WHERE tt.company_id=$1 AND tt.reservation_id=$2 AND tt.match_status<>'excluded'
		ORDER BY tt.transaction_at ASC
	`, cid, rid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []TollTx{}
	for rows.Next() {
		var t TollTx
		if err := rows.Scan(&t.ID, &t.ProviderTxID, &t.Provider, &t.TransponderRef,
			&t.TransponderLabel, &t.VehicleLabel, &t.ReservationNo, &t.CustomerName,
			&t.LicensePlate, &t.Location, &t.EntryPlaza, &t.ExitPlaza,
			&t.TransactionAt, &t.AmountCents, &t.AdminFeeCents,
			&t.MatchStatus, &t.BillingStatus, &t.ReservationID, &t.VehicleID); err != nil {
			continue
		}
		t.TotalCents = t.AmountCents + t.AdminFeeCents
		out = append(out, t)
	}
	return out
}

// =============================================================================
// Matching engine
// =============================================================================

// matchOne looks at a toll transaction and figures out which vehicle was
// carrying the transponder at that moment, and which reservation was active
// for that vehicle. Returns (reservation, vehicle, customer, status).
//   status = "matched"         → single reservation covered the vehicle at that time
//            "company_expense" → vehicle wasn't rented (company drive, maintenance)
//            "needs_review"    → vehicle unknown, or multiple reservations overlap
//            "pending"         → nothing matches yet; wait for more data
func (h *Handler) matchOne(r *http.Request, cid uuid.UUID, txID string) (*uuid.UUID, *uuid.UUID, *uuid.UUID, string, error) {
	// Load the transaction's transponder_ref + timestamp + any explicit vehicle_id.
	var tagRef *string
	var when time.Time
	var curVehicle *uuid.UUID
	err := h.Pool.QueryRow(r.Context(),
		`SELECT transponder_ref, transaction_at, vehicle_id
		   FROM toll_transactions WHERE company_id=$1 AND id=$2`, cid, txID).Scan(&tagRef, &when, &curVehicle)
	if err != nil {
		return nil, nil, nil, "", err
	}

	vehicleID := curVehicle
	// Resolve the vehicle from the transponder assignment that was active at `when`.
	if vehicleID == nil && tagRef != nil && *tagRef != "" {
		var vid uuid.UUID
		err := h.Pool.QueryRow(r.Context(), `
			SELECT va.vehicle_id
			FROM vehicle_transponder_assignments va
			JOIN toll_transponders t ON t.id=va.transponder_id
			WHERE t.company_id=$1 AND t.transponder_id=$2
			  AND va.assigned_at <= $3 AND (va.removed_at IS NULL OR va.removed_at > $3)
			ORDER BY va.assigned_at DESC
			LIMIT 1
		`, cid, *tagRef, when).Scan(&vid)
		if err == nil {
			vehicleID = &vid
		}
	}

	if vehicleID == nil {
		return nil, nil, nil, "needs_review", nil
	}

	// Count how many non-cancelled reservations overlap this moment for this vehicle.
	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, customer_id FROM reservations
		WHERE company_id=$1 AND vehicle_id=$2
		  AND pickup_at <= $3 AND return_at >= $3
		  AND status NOT IN ('cancelled','no_show')
	`, cid, *vehicleID, when)
	if err != nil {
		return nil, vehicleID, nil, "needs_review", nil
	}
	defer rows.Close()

	matches := []struct{ rid, cuid uuid.UUID }{}
	for rows.Next() {
		var rid, cuid uuid.UUID
		if err := rows.Scan(&rid, &cuid); err == nil {
			matches = append(matches, struct{ rid, cuid uuid.UUID }{rid, cuid})
		}
	}
	switch len(matches) {
	case 0:
		return nil, vehicleID, nil, "company_expense", nil
	case 1:
		return &matches[0].rid, vehicleID, &matches[0].cuid, "matched", nil
	default:
		return nil, vehicleID, nil, "needs_review", nil
	}
}

// =============================================================================
// Utilities
// =============================================================================

func mapRow(headers, row []string) map[string]string {
	out := map[string]string{}
	for i, h := range headers {
		if i >= len(row) {
			break
		}
		out[h] = row[i]
	}
	return out
}

// parseTollDateTime tries "combined", then "date" + "time". Accepts several
// US-style formats commonly seen in E-ZPass VA exports.
func parseTollDateTime(combined, date, tm string) (time.Time, error) {
	formats := []string{
		"01/02/2006 15:04:05",
		"01/02/2006 03:04:05 PM",
		"01/02/2006 3:04:05 PM",
		"01/02/2006 15:04",
		"1/2/2006 15:04",
		"1/2/2006 3:04 PM",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05Z07:00",
	}
	if combined != "" {
		for _, f := range formats {
			if t, err := time.Parse(f, combined); err == nil {
				return t, nil
			}
		}
	}
	if date != "" && tm != "" {
		joined := date + " " + tm
		for _, f := range formats {
			if t, err := time.Parse(f, joined); err == nil {
				return t, nil
			}
		}
	}
	if date != "" {
		for _, f := range []string{"01/02/2006", "1/2/2006", "2006-01-02"} {
			if t, err := time.Parse(f, date); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("unparseable date/time: %q %q %q", combined, date, tm)
}

func parseAmountCents(s string) int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = "-" + strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	}
	if strings.Contains(s, ".") {
		f, _ := strconv.ParseFloat(s, 64)
		return int(f*100 + 0.5)
	}
	n, _ := strconv.Atoi(s)
	return n * 100
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func nilIfZero(acc *TollAccount) any {
	if acc == nil {
		return nil
	}
	return acc.ID
}
