package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ReportBundle is the rich dataset passed to the reports template and the JSON
// endpoint used by Chart.js on the client.
type ReportBundle struct {
	// KPI tiles.
	TotalRevenueCents int `json:"totalRevenueCents"`
	MonthRevenueCents int `json:"monthRevenueCents"`
	YearRevenueCents  int `json:"yearRevenueCents"`
	TodayRevenueCents int `json:"todayRevenueCents"`
	OutstandingCents  int `json:"outstandingCents"`
	MaintenanceCents  int `json:"maintenanceCents"`
	AvgBookingCents   int `json:"avgBookingCents"`

	TotalBookings  int `json:"totalBookings"`
	MonthBookings  int `json:"monthBookings"`
	ActiveRentals  int `json:"activeRentals"`
	Cancelled      int `json:"cancelled"`
	TotalFleet     int `json:"totalFleet"`
	AvailableNow   int `json:"availableNow"`
	UtilizationPct int `json:"utilizationPct"`
	TotalCustomers int `json:"totalCustomers"`
	NewCustomers30 int `json:"newCustomers30"`

	// Time series.
	Daily30     []BucketRow `json:"daily30"`     // last 30 days: revenue + bookings
	Monthly12   []BucketRow `json:"monthly12"`   // last 12 months
	Yearly5     []BucketRow `json:"yearly5"`     // last 5 years
	Weekday     []BucketRow `json:"weekday"`     // Mon..Sun share of bookings
	MonthOfYear []BucketRow `json:"monthOfYear"` // Jan..Dec share of bookings (yearly pattern)

	// Distributions.
	StatusMix     []BucketRow `json:"statusMix"`
	InsuranceMix  []BucketRow `json:"insuranceMix"`
	BranchRevenue []BucketRow `json:"branchRevenue"`
	TopVehicles   []BucketRow `json:"topVehicles"`
	TopCustomers  []BucketRow `json:"topCustomers"`
}

// BucketRow is a label + a value (bookings count or revenue cents).
type BucketRow struct {
	Label        string `json:"label"`
	Count        int    `json:"count"`
	RevenueCents int    `json:"revenueCents,omitempty"`
}

func (h *Handler) loadReportBundle(ctx context.Context, cid uuid.UUID) *ReportBundle {
	b := &ReportBundle{}

	// Headline totals.
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE company_id=$1 AND status='succeeded'`, cid,
	).Scan(&b.TotalRevenueCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_cents),0) FROM payments
		   WHERE company_id=$1 AND status='succeeded' AND created_at >= date_trunc('month', NOW())`, cid,
	).Scan(&b.MonthRevenueCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_cents),0) FROM payments
		   WHERE company_id=$1 AND status='succeeded' AND created_at >= date_trunc('year', NOW())`, cid,
	).Scan(&b.YearRevenueCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount_cents),0) FROM payments
		   WHERE company_id=$1 AND status='succeeded' AND created_at::date = CURRENT_DATE`, cid,
	).Scan(&b.TodayRevenueCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(GREATEST(total_cents-paid_cents,0)),0) FROM reservations
		   WHERE company_id=$1 AND status IN ('pending','confirmed','checked_out')`, cid,
	).Scan(&b.OutstandingCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_cents),0) FROM maintenance_records WHERE company_id=$1`, cid,
	).Scan(&b.MaintenanceCents)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COALESCE(ROUND(AVG(total_cents))::int, 0) FROM reservations
		   WHERE company_id=$1 AND status NOT IN ('cancelled','no_show')`, cid,
	).Scan(&b.AvgBookingCents)

	// Counts.
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM reservations WHERE company_id=$1`, cid).Scan(&b.TotalBookings)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND created_at >= date_trunc('month', NOW())`,
		cid).Scan(&b.MonthBookings)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='checked_out'`,
		cid).Scan(&b.ActiveRentals)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='cancelled'`,
		cid).Scan(&b.Cancelled)
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, cid).Scan(&b.TotalFleet)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vehicles WHERE company_id=$1 AND status='available'`,
		cid).Scan(&b.AvailableNow)
	if b.TotalFleet > 0 {
		b.UtilizationPct = 100 * (b.TotalFleet - b.AvailableNow) / b.TotalFleet
	}
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM customers WHERE company_id=$1`, cid).Scan(&b.TotalCustomers)
	_ = h.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM customers WHERE company_id=$1 AND created_at >= NOW() - INTERVAL '30 days'`,
		cid).Scan(&b.NewCustomers30)

	// Daily revenue + bookings (last 30 days, zero-filled).
	b.Daily30 = h.timeSeries(ctx, cid, `
		SELECT TO_CHAR(d, 'DD Mon') AS label,
		       COALESCE(COUNT(r.id),0) AS cnt,
		       COALESCE(SUM(p.amount_cents),0) AS rev
		FROM generate_series(CURRENT_DATE - INTERVAL '29 days', CURRENT_DATE, INTERVAL '1 day') d(d)
		LEFT JOIN reservations r ON r.company_id=$1 AND r.created_at::date = d::date
		LEFT JOIN payments p ON p.company_id=$1 AND p.status='succeeded' AND p.created_at::date = d::date
		GROUP BY d ORDER BY d`)

	// Monthly (last 12 months).
	b.Monthly12 = h.timeSeries(ctx, cid, `
		SELECT TO_CHAR(d, 'Mon YY') AS label,
		       COALESCE(COUNT(r.id),0) AS cnt,
		       COALESCE(SUM(p.amount_cents),0) AS rev
		FROM generate_series(date_trunc('month', NOW()) - INTERVAL '11 months', date_trunc('month', NOW()), INTERVAL '1 month') d(d)
		LEFT JOIN reservations r ON r.company_id=$1 AND date_trunc('month', r.created_at) = d
		LEFT JOIN payments p ON p.company_id=$1 AND p.status='succeeded' AND date_trunc('month', p.created_at) = d
		GROUP BY d ORDER BY d`)

	// Yearly (last 5 years).
	b.Yearly5 = h.timeSeries(ctx, cid, `
		SELECT TO_CHAR(d, 'YYYY') AS label,
		       COALESCE(COUNT(r.id),0) AS cnt,
		       COALESCE(SUM(p.amount_cents),0) AS rev
		FROM generate_series(date_trunc('year', NOW()) - INTERVAL '4 years', date_trunc('year', NOW()), INTERVAL '1 year') d(d)
		LEFT JOIN reservations r ON r.company_id=$1 AND date_trunc('year', r.created_at) = d
		LEFT JOIN payments p ON p.company_id=$1 AND p.status='succeeded' AND date_trunc('year', p.created_at) = d
		GROUP BY d ORDER BY d`)

	// Weekday share of bookings (Mon..Sun).
	wdNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	wdCounts := make([]int, 7)
	rows, _ := h.Pool.Query(ctx, `
		SELECT EXTRACT(ISODOW FROM pickup_at)::int AS d, COUNT(*)
		FROM reservations WHERE company_id=$1
		GROUP BY 1 ORDER BY 1`, cid)
	if rows != nil {
		for rows.Next() {
			var d, c int
			_ = rows.Scan(&d, &c)
			if d >= 1 && d <= 7 {
				wdCounts[d-1] = c
			}
		}
		rows.Close()
	}
	for i, n := range wdNames {
		b.Weekday = append(b.Weekday, BucketRow{Label: n, Count: wdCounts[i]})
	}

	// Month-of-year share of bookings (Jan..Dec).
	moNames := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	moCounts := make([]int, 12)
	rows, _ = h.Pool.Query(ctx, `
		SELECT EXTRACT(MONTH FROM pickup_at)::int AS m, COUNT(*)
		FROM reservations WHERE company_id=$1
		GROUP BY 1 ORDER BY 1`, cid)
	if rows != nil {
		for rows.Next() {
			var m, c int
			_ = rows.Scan(&m, &c)
			if m >= 1 && m <= 12 {
				moCounts[m-1] = c
			}
		}
		rows.Close()
	}
	for i, n := range moNames {
		b.MonthOfYear = append(b.MonthOfYear, BucketRow{Label: n, Count: moCounts[i]})
	}

	// Status mix (pie).
	b.StatusMix = h.bucket(ctx, `
		SELECT COALESCE(status,'unknown') AS label, COUNT(*)::int
		FROM reservations WHERE company_id=$1
		GROUP BY 1 ORDER BY 2 DESC`, cid)

	// Insurance uptake.
	b.InsuranceMix = h.bucket(ctx, `
		SELECT CASE
		         WHEN insurance_code IS NULL OR insurance_code='' OR insurance_code='none' THEN 'Declined'
		         ELSE COALESCE((SELECT name FROM insurance_catalog ic WHERE ic.company_id=r.company_id AND ic.code=r.insurance_code), r.insurance_code)
		       END AS label,
		       COUNT(*)::int
		FROM reservations r WHERE company_id=$1
		GROUP BY 1 ORDER BY 2 DESC`, cid)

	// Branch revenue (horizontal bar).
	b.BranchRevenue = h.bucketRev(ctx, `
		SELECT COALESCE(br.name,'(unassigned)') AS label,
		       COUNT(r.id)::int AS cnt,
		       COALESCE(SUM(r.total_cents),0)::int AS rev
		FROM reservations r
		LEFT JOIN branches br ON br.id=r.pickup_branch_id
		WHERE r.company_id=$1
		GROUP BY br.name ORDER BY rev DESC LIMIT 8`, cid)

	// Top rented vehicles.
	b.TopVehicles = h.bucketRev(ctx, `
		SELECT COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,'')||
		         CASE WHEN v.license_plate IS NULL THEN '' ELSE ' ('||v.license_plate||')' END AS label,
		       COUNT(r.id)::int AS cnt,
		       COALESCE(SUM(r.total_cents),0)::int AS rev
		FROM reservations r
		JOIN vehicles v ON v.id=r.vehicle_id
		WHERE r.company_id=$1 AND r.status NOT IN ('cancelled','no_show')
		GROUP BY v.id, v.year, v.make, v.model, v.license_plate
		ORDER BY cnt DESC LIMIT 10`, cid)

	// Top customers.
	b.TopCustomers = h.bucketRev(ctx, `
		SELECT (cu.first_name||' '||cu.last_name) AS label,
		       COUNT(r.id)::int AS cnt,
		       COALESCE(SUM(r.total_cents),0)::int AS rev
		FROM reservations r
		JOIN customers cu ON cu.id=r.customer_id
		WHERE r.company_id=$1
		GROUP BY cu.id, cu.first_name, cu.last_name
		ORDER BY cnt DESC LIMIT 10`, cid)

	return b
}

// timeSeries runs a query that returns (label, count, revenue_cents).
func (h *Handler) timeSeries(ctx context.Context, cid uuid.UUID, q string) []BucketRow {
	out := []BucketRow{}
	rows, err := h.Pool.Query(ctx, q, cid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var br BucketRow
		if err := rows.Scan(&br.Label, &br.Count, &br.RevenueCents); err == nil {
			out = append(out, br)
		}
	}
	return out
}

func (h *Handler) bucket(ctx context.Context, q string, cid uuid.UUID) []BucketRow {
	out := []BucketRow{}
	rows, err := h.Pool.Query(ctx, q, cid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var br BucketRow
		if err := rows.Scan(&br.Label, &br.Count); err == nil {
			out = append(out, br)
		}
	}
	return out
}

func (h *Handler) bucketRev(ctx context.Context, q string, cid uuid.UUID) []BucketRow {
	out := []BucketRow{}
	rows, err := h.Pool.Query(ctx, q, cid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var br BucketRow
		if err := rows.Scan(&br.Label, &br.Count, &br.RevenueCents); err == nil {
			out = append(out, br)
		}
	}
	return out
}

// ReportsPro renders the pro reports page. Replaces the old Reports handler.
func (h *Handler) ReportsPro(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	bundle := h.loadReportBundle(r.Context(), cid)
	raw, _ := json.Marshal(bundle)

	// Primary branch (for the printed header).
	var branchName, branchAddr, companyName string
	_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM companies WHERE id=$1`, cid).Scan(&companyName)
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT name, COALESCE(address,'')||CASE WHEN city IS NULL OR city='' THEN '' ELSE ', '||city END
		   FROM branches WHERE company_id=$1 AND is_active ORDER BY created_at LIMIT 1`,
		cid).Scan(&branchName, &branchAddr)

	h.render(w, r, "admin/reports.html", "Reports", "reports", map[string]any{
		"B":           bundle,
		"BJSON":       template.JS(raw),
		"CompanyName": companyName,
		"BranchName":  branchName,
		"BranchAddr":  branchAddr,
		"GeneratedAt": time.Now().Format("Mon 02 Jan 2006 · 15:04"),
	})
}

// ReportsPrint is the standalone print-friendly view. Opens in a new tab;
// the user hits Ctrl-P (or the Print button on-page) for a PDF.
func (h *Handler) ReportsPrint(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	bundle := h.loadReportBundle(r.Context(), cid)
	raw, _ := json.Marshal(bundle)

	var branchName, branchAddr, companyName string
	_ = h.Pool.QueryRow(r.Context(), `SELECT name FROM companies WHERE id=$1`, cid).Scan(&companyName)
	_ = h.Pool.QueryRow(r.Context(),
		`SELECT name, COALESCE(address,'')||CASE WHEN city IS NULL OR city='' THEN '' ELSE ', '||city END
		   FROM branches WHERE company_id=$1 AND is_active ORDER BY created_at LIMIT 1`,
		cid).Scan(&branchName, &branchAddr)

	// Render the same template with Print=true so the layout drops navigation
	// chrome and auto-triggers window.print() on load.
	h.render(w, r, "admin/reports.html", "Report "+time.Now().Format("2006-01-02"), "reports", map[string]any{
		"B":           bundle,
		"BJSON":       template.JS(raw),
		"CompanyName": companyName,
		"BranchName":  branchName,
		"BranchAddr":  branchAddr,
		"GeneratedAt": time.Now().Format("Mon 02 Jan 2006 · 15:04"),
		"Print":       true,
	})
}

// ReportsCSVPro writes a wide CSV with the real metrics.
func (h *Handler) ReportsCSVPro(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	b := h.loadReportBundle(r.Context(), cid)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="report-`+time.Now().Format("2006-01-02")+`.csv"`)
	fmt.Fprintln(w, "section,label,count,revenue_cents")
	fmt.Fprintf(w, "kpi,total revenue,,%d\n", b.TotalRevenueCents)
	fmt.Fprintf(w, "kpi,month revenue,,%d\n", b.MonthRevenueCents)
	fmt.Fprintf(w, "kpi,year revenue,,%d\n", b.YearRevenueCents)
	fmt.Fprintf(w, "kpi,today revenue,,%d\n", b.TodayRevenueCents)
	fmt.Fprintf(w, "kpi,outstanding,,%d\n", b.OutstandingCents)
	fmt.Fprintf(w, "kpi,maintenance,,%d\n", b.MaintenanceCents)
	fmt.Fprintf(w, "kpi,avg booking,,%d\n", b.AvgBookingCents)
	fmt.Fprintf(w, "kpi,total bookings,%d,\n", b.TotalBookings)
	fmt.Fprintf(w, "kpi,month bookings,%d,\n", b.MonthBookings)
	fmt.Fprintf(w, "kpi,active rentals,%d,\n", b.ActiveRentals)
	fmt.Fprintf(w, "kpi,cancelled,%d,\n", b.Cancelled)
	fmt.Fprintf(w, "kpi,fleet size,%d,\n", b.TotalFleet)
	fmt.Fprintf(w, "kpi,available now,%d,\n", b.AvailableNow)
	fmt.Fprintf(w, "kpi,utilization pct,%d,\n", b.UtilizationPct)
	fmt.Fprintf(w, "kpi,customers,%d,\n", b.TotalCustomers)
	fmt.Fprintf(w, "kpi,new customers 30d,%d,\n", b.NewCustomers30)
	writeRows := func(section string, rs []BucketRow) {
		for _, r := range rs {
			fmt.Fprintf(w, "%s,%q,%d,%d\n", section, r.Label, r.Count, r.RevenueCents)
		}
	}
	writeRows("daily_30", b.Daily30)
	writeRows("monthly_12", b.Monthly12)
	writeRows("yearly_5", b.Yearly5)
	writeRows("weekday", b.Weekday)
	writeRows("month_of_year", b.MonthOfYear)
	writeRows("status_mix", b.StatusMix)
	writeRows("insurance_mix", b.InsuranceMix)
	writeRows("branch_revenue", b.BranchRevenue)
	writeRows("top_vehicles", b.TopVehicles)
	writeRows("top_customers", b.TopCustomers)
}

