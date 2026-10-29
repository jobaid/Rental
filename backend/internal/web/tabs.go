package web

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

// Daily Manifest: redirected to the dashboard tab so navigation stays in-page.
// Kept for backward-compat with any bookmarks or deep links.
func (h *Handler) DailyManifestLegacy(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin?view=daily-manifest", http.StatusSeeOther)
}

// Daily Manifest full-page (unused since the dashboard tab replaced it; still available for direct URL access).
func (h *Handler) DailyManifest(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	today := time.Now().Truncate(24 * time.Hour)
	tomorrow := today.AddDate(0, 0, 1)
	dayAfter := today.AddDate(0, 0, 2)

	type Row struct {
		ID            uuid.UUID
		BookingNumber string
		Customer      *string
		CustomerEmail *string
		CustomerPhone *string
		Vehicle       *string
		Branch        *string
		PickupAt      time.Time
		ReturnAt      time.Time
		Status        string
		TotalCents    int
		PaidCents     int
		OverdueBy     string
	}

	fetch := func(sql string, args ...any) []Row {
		rows, err := h.Pool.Query(r.Context(), sql, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()
		out := []Row{}
		for rows.Next() {
			var rr Row
			if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Customer, &rr.CustomerEmail, &rr.CustomerPhone, &rr.Vehicle, &rr.Branch, &rr.PickupAt, &rr.ReturnAt, &rr.Status, &rr.TotalCents, &rr.PaidCents); err == nil {
				out = append(out, rr)
			}
		}
		return out
	}

	baseSelect := `
		SELECT r.id, r.booking_number,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE cu.first_name||' '||cu.last_name END,
		       cu.email, cu.phone,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       pb.name,
		       r.pickup_at, r.return_at, r.status, r.total_cents, r.paid_cents
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN branches pb ON pb.id=r.pickup_branch_id
		WHERE r.company_id=$1 `

	todayPickups := fetch(baseSelect+`AND r.pickup_at >= $2 AND r.pickup_at < $3 ORDER BY r.pickup_at`, cid, today, tomorrow)
	todayReturns := fetch(baseSelect+`AND r.return_at >= $2 AND r.return_at < $3 ORDER BY r.return_at`, cid, today, tomorrow)
	tomorrowPickups := fetch(baseSelect+`AND r.pickup_at >= $2 AND r.pickup_at < $3 ORDER BY r.pickup_at`, cid, tomorrow, dayAfter)
	tomorrowReturns := fetch(baseSelect+`AND r.return_at >= $2 AND r.return_at < $3 ORDER BY r.return_at`, cid, tomorrow, dayAfter)
	activeRentals := fetch(baseSelect+`AND r.status='checked_out' ORDER BY r.return_at`, cid)
	overdueRentals := fetch(baseSelect+`AND r.status='checked_out' AND r.return_at < NOW() ORDER BY r.return_at`, cid)
	// Compute overdue duration display.
	now := time.Now()
	for i := range overdueRentals {
		d := now.Sub(overdueRentals[i].ReturnAt)
		if d < time.Hour {
			overdueRentals[i].OverdueBy = "under 1h"
		} else if d < 24*time.Hour {
			overdueRentals[i].OverdueBy = formatHours(d.Hours()) + "h"
		} else {
			overdueRentals[i].OverdueBy = formatHours(d.Hours()/24) + "d"
		}
	}

	h.render(w, r, "admin/daily_manifest.html", "Daily manifest", "dashboard", map[string]any{
		"TodayPickups":    todayPickups,
		"TodayReturns":    todayReturns,
		"TomorrowPickups": tomorrowPickups,
		"TomorrowReturns": tomorrowReturns,
		"ActiveRentals":   activeRentals,
		"OverdueRentals":  overdueRentals,
		"Today":           today.Format("Mon 02 Jan"),
		"Tomorrow":        tomorrow.Format("Mon 02 Jan"),
	})
}

func formatHours(f float64) string {
	if f < 10 {
		return "" + stringFixed(f, 1)
	}
	return stringFixed(f, 0)
}
func stringFixed(f float64, p int) string {
	if p == 0 {
		return itoaInt(int(f))
	}
	whole := int(f)
	frac := int((f - float64(whole)) * 10)
	return itoaInt(whole) + "." + itoaInt(frac)
}
func itoaInt(n int) string {
	if n < 0 {
		return "-" + itoaInt(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoaInt(n/10) + string(rune('0'+n%10))
}

// Getting Started: compute a real completion % from the DB.
type SetupTask struct {
	Key   string
	Label string
	Done  bool
	Hint  string
	Href  string
}

func (h *Handler) GettingStarted(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	ctx := r.Context()
	check := func(sql string, args ...any) bool {
		var n int
		_ = h.Pool.QueryRow(ctx, sql, args...).Scan(&n)
		return n > 0
	}
	tasks := []SetupTask{
		{"locations", "Add at least one pickup/return location", check(`SELECT COUNT(*) FROM branches WHERE company_id=$1 AND is_active`, cid), "Branches represent pickup & return points.", "/admin/branches"},
		{"vehicles", "Add vehicles to your fleet", check(`SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, cid), "At least one vehicle must exist for bookings to show.", "/admin/vehicles"},
		{"photos", "Upload a photo for each vehicle", check(`SELECT COUNT(*) FROM vehicle_photos WHERE company_id=$1`, cid), "Primary photos appear on the customer search page.", "/admin/vehicles"},
		{"rates", "Configure rental rates (default or seasonal)", check(`SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND daily_rate_cents > 0`, cid), "Each vehicle has a default rate; add period overrides for seasons.", "/admin/vehicles"},
		{"tax", "Set your tax rate", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='tax' AND (value->>'ratePct')::int > 0`, cid), "Applied to subtotal of every new booking.", "/admin/settings#tax"},
		{"addons", "Review optional add-ons", check(`SELECT COUNT(*) FROM addon_catalog WHERE company_id=$1 AND is_active`, cid), "Child seat, GPS, Wi-Fi, additional driver, etc. Seeded by default.", "/admin/communications/templates"},
		{"insurance", "Review insurance options", check(`SELECT COUNT(*) FROM insurance_catalog WHERE company_id=$1 AND is_active`, cid), "Decline / Basic / Premium plans. Seeded by default.", "/admin/communications/templates"},
		{"smtp", "Configure SMTP for email", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='smtp' AND value->>'host' != ''`, cid), "Required for booking confirmations and the Communication Center.", "/admin/settings#smtp"},
		{"templates", "Review email templates", check(`SELECT COUNT(*) FROM communication_templates WHERE company_id=$1 AND is_active`, cid), "14 default templates are seeded automatically.", "/admin/communications/templates"},
		{"general", "Set business name, phone, address", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='general' AND value->>'businessName' != ''`, cid), "Shown on invoices, agreements, and emails.", "/admin/settings"},
	}
	done := 0
	for _, t := range tasks {
		if t.Done {
			done++
		}
	}
	pct := 0
	if len(tasks) > 0 {
		pct = done * 100 / len(tasks)
	}
	h.render(w, r, "admin/getting_started.html", "Getting Started", "dashboard", map[string]any{
		"Tasks":    tasks,
		"DoneN":    done,
		"TotalN":   len(tasks),
		"Progress": pct,
	})
}

// Extras: list addon_catalog + insurance_catalog (real DB rows, no fake data).
func (h *Handler) Extras(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	type AddonRow struct {
		Code, Name string
		Desc       *string
		PriceCents int
		PricePer   string
		Active     bool
	}
	type InsRow struct {
		Code, Name string
		Desc       *string
		PerDay     int
		Deductible int
		Default    bool
		Active     bool
	}
	rows1, _ := h.Pool.Query(r.Context(), `SELECT code, name, description, price_cents, price_per, is_active FROM addon_catalog WHERE company_id=$1 ORDER BY display_order, name`, cid)
	addons := []AddonRow{}
	if rows1 != nil {
		for rows1.Next() {
			var a AddonRow
			_ = rows1.Scan(&a.Code, &a.Name, &a.Desc, &a.PriceCents, &a.PricePer, &a.Active)
			addons = append(addons, a)
		}
		rows1.Close()
	}
	rows2, _ := h.Pool.Query(r.Context(), `SELECT code, name, description, price_per_day_cents, deductible_cents, is_default, is_active FROM insurance_catalog WHERE company_id=$1 ORDER BY price_per_day_cents`, cid)
	insurance := []InsRow{}
	if rows2 != nil {
		for rows2.Next() {
			var i InsRow
			_ = rows2.Scan(&i.Code, &i.Name, &i.Desc, &i.PerDay, &i.Deductible, &i.Default, &i.Active)
			insurance = append(insurance, i)
		}
		rows2.Close()
	}
	h.render(w, r, "admin/extras.html", "Extras", "dashboard", map[string]any{
		"Addons": addons, "Insurance": insurance,
	})
}

// Attempts: honest empty state (no table in schema).
func (h *Handler) Attempts(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "admin/attempts.html", "Attempts", "dashboard", nil)
}

// Commissions: honest empty state (no table in schema).
func (h *Handler) Commissions(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "admin/commissions.html", "Commissions", "dashboard", nil)
}
