package web

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

type manifestRow struct {
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

func (h *Handler) loadDailyManifest(r *http.Request, cid uuid.UUID, data map[string]any) {
	today := time.Now().Truncate(24 * time.Hour)
	tomorrow := today.AddDate(0, 0, 1)
	dayAfter := today.AddDate(0, 0, 2)

	fetch := func(sql string, args ...any) []manifestRow {
		rows, err := h.Pool.Query(r.Context(), sql, args...)
		if err != nil {
			return nil
		}
		defer rows.Close()
		out := []manifestRow{}
		for rows.Next() {
			var rr manifestRow
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

	data["TodayPickups"] = fetch(baseSelect+`AND r.pickup_at >= $2 AND r.pickup_at < $3 ORDER BY r.pickup_at`, cid, today, tomorrow)
	data["TodayReturns"] = fetch(baseSelect+`AND r.return_at >= $2 AND r.return_at < $3 ORDER BY r.return_at`, cid, today, tomorrow)
	data["TomorrowPickups"] = fetch(baseSelect+`AND r.pickup_at >= $2 AND r.pickup_at < $3 ORDER BY r.pickup_at`, cid, tomorrow, dayAfter)
	data["TomorrowReturns"] = fetch(baseSelect+`AND r.return_at >= $2 AND r.return_at < $3 ORDER BY r.return_at`, cid, tomorrow, dayAfter)
	data["ActiveList"] = fetch(baseSelect+`AND r.status='checked_out' ORDER BY r.return_at`, cid)
	od := fetch(baseSelect+`AND r.status='checked_out' AND r.return_at < NOW() ORDER BY r.return_at`, cid)
	now := time.Now()
	for i := range od {
		d := now.Sub(od[i].ReturnAt)
		if d < time.Hour {
			od[i].OverdueBy = "<1h"
		} else if d < 24*time.Hour {
			od[i].OverdueBy = formatHours(d.Hours()) + "h"
		} else {
			od[i].OverdueBy = formatHours(d.Hours()/24) + "d"
		}
	}
	data["OverdueList"] = od
	data["Today"] = today.Format("Mon 02 Jan")
}

func (h *Handler) loadGettingStarted(r *http.Request, cid uuid.UUID, data map[string]any) {
	ctx := r.Context()
	check := func(sql string, args ...any) bool {
		var n int
		_ = h.Pool.QueryRow(ctx, sql, args...).Scan(&n)
		return n > 0
	}
	tasks := []SetupTask{
		{"locations", "Add pickup/return locations", check(`SELECT COUNT(*) FROM branches WHERE company_id=$1 AND is_active`, cid), "Branches represent pickup & return points.", "/admin/branches"},
		{"vehicles", "Add vehicles to your fleet", check(`SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, cid), "At least one vehicle must exist for bookings to show.", "/admin/vehicles"},
		{"photos", "Upload a photo per vehicle", check(`SELECT COUNT(*) FROM vehicle_photos WHERE company_id=$1`, cid), "Primary photos appear on the customer search page.", "/admin/vehicles"},
		{"rates", "Configure rental rates", check(`SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND daily_rate_cents > 0`, cid), "Add seasonal periods on each vehicle for peak pricing.", "/admin/vehicles"},
		{"tax", "Set your tax rate", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='tax' AND (value->>'ratePct')::int > 0`, cid), "Applied to subtotal of every new booking.", "/admin/settings#tax"},
		{"addons", "Review optional add-ons", check(`SELECT COUNT(*) FROM addon_catalog WHERE company_id=$1 AND is_active`, cid), "Child seat, GPS, Wi-Fi. Seeded by default.", "/admin?view=extras"},
		{"insurance", "Review insurance options", check(`SELECT COUNT(*) FROM insurance_catalog WHERE company_id=$1 AND is_active`, cid), "Decline / Basic / Premium. Seeded by default.", "/admin?view=extras"},
		{"smtp", "Configure SMTP for email", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='smtp' AND value->>'host' != ''`, cid), "Required for booking confirmations and the Communication Center.", "/admin/settings#smtp"},
		{"templates", "Review email templates", check(`SELECT COUNT(*) FROM communication_templates WHERE company_id=$1 AND is_active`, cid), "14 default templates seeded automatically.", "/admin/communications/templates"},
		{"general", "Set business name & contact", check(`SELECT COUNT(*) FROM settings WHERE company_id=$1 AND key='general' AND value->>'businessName' != ''`, cid), "Shown on invoices, agreements, and emails.", "/admin/settings"},
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
	data["Tasks"] = tasks
	data["DoneN"] = done
	data["TotalN"] = len(tasks)
	data["Progress"] = pct
}

func (h *Handler) loadBookingsPanel(r *http.Request, cid uuid.UUID, data map[string]any) {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status,
		       CASE WHEN cu.id IS NULL THEN NULL ELSE cu.first_name||' '||cu.last_name END,
		       CASE WHEN v.id IS NULL THEN NULL ELSE COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'') END,
		       r.pickup_at, r.return_at, r.total_cents
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.company_id=$1 ORDER BY r.pickup_at DESC LIMIT 20
	`, cid)
	type row struct {
		ID            uuid.UUID
		BookingNumber string
		Status        string
		Customer      *string
		Vehicle       *string
		PickupAt      time.Time
		ReturnAt      time.Time
		TotalCents    int
	}
	list := []row{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rr row
			if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Status, &rr.Customer, &rr.Vehicle, &rr.PickupAt, &rr.ReturnAt, &rr.TotalCents); err == nil {
				list = append(list, rr)
			}
		}
	}
	data["BookingsList"] = list
}

func (h *Handler) loadAvailabilityPanel(r *http.Request, cid uuid.UUID, data map[string]any) {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at,
		       COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.license_plate, cu.first_name, cu.last_name
		FROM reservations r
		LEFT JOIN vehicles v ON v.id=r.vehicle_id
		LEFT JOIN customers cu ON cu.id=r.customer_id
		WHERE r.company_id=$1 AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		ORDER BY r.pickup_at ASC
	`, cid)
	type rc struct {
		ID            uuid.UUID
		BookingNumber string
		Status        string
		Overdue       bool
		PickupAt      time.Time
		ReturnAt      time.Time
		Vehicle       string
		LicensePlate  *string
		CustomerFirst *string
		CustomerLast  *string
	}
	list := []rc{}
	now := time.Now()
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var rr rc
			if err := rows.Scan(&rr.ID, &rr.BookingNumber, &rr.Status, &rr.PickupAt, &rr.ReturnAt, &rr.Vehicle, &rr.LicensePlate, &rr.CustomerFirst, &rr.CustomerLast); err == nil {
				rr.Overdue = rr.Status == "checked_out" && rr.ReturnAt.Before(now)
				list = append(list, rr)
			}
		}
	}
	data["AvailabilityList"] = list
}

func (h *Handler) loadFleetPanel(r *http.Request, cid uuid.UUID, data map[string]any) {
	rows, err := h.Pool.Query(r.Context(), `
		SELECT v.id, v.fleet_number, v.license_plate, COALESCE(v.make,''), COALESCE(v.model,''), COALESCE(v.year,0),
		       v.daily_rate_cents, v.status, v.odometer_km,
		       COALESCE((SELECT file_path FROM vehicle_photos WHERE vehicle_id=v.id ORDER BY is_primary DESC LIMIT 1), '')
		FROM vehicles v WHERE v.company_id=$1 ORDER BY v.created_at DESC
	`, cid)
	type row struct {
		ID             uuid.UUID
		FleetNumber    *string
		LicensePlate   *string
		Make, Model    string
		Year           int
		DailyRateCents int
		Status         string
		OdometerKm     *int
		Photo          string
	}
	list := []row{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var v row
			var photo string
			if err := rows.Scan(&v.ID, &v.FleetNumber, &v.LicensePlate, &v.Make, &v.Model, &v.Year, &v.DailyRateCents, &v.Status, &v.OdometerKm, &photo); err == nil {
				if photo != "" {
					v.Photo = "/uploads/" + photo
				}
				list = append(list, v)
			}
		}
	}
	data["FleetList"] = list
}

func (h *Handler) loadExtrasPanel(r *http.Request, cid uuid.UUID, data map[string]any) {
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
	ins := []InsRow{}
	if rows2 != nil {
		for rows2.Next() {
			var i InsRow
			_ = rows2.Scan(&i.Code, &i.Name, &i.Desc, &i.PerDay, &i.Deductible, &i.Default, &i.Active)
			ins = append(ins, i)
		}
		rows2.Close()
	}
	data["Addons"] = addons
	data["Insurance"] = ins
}
