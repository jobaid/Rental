package web

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type KPI struct {
	Label    string
	Value    string
	Raw      float64
	Delta    float64 // % change vs previous period; 0 if N/A
	HasDelta bool
	Positive bool // true = green up-arrow, false = red
	Hint     string
}

type ChartPoint struct {
	Label string
	Value float64
}

type ChartBar struct {
	Label string
	Value float64
	Color string
	Hint  string
}

// resolveRange parses the `preset` + optional from/to and returns [curStart, curEnd, prevStart, prevEnd, label].
func resolveRange(preset, fromStr, toStr string) (curStart, curEnd, prevStart, prevEnd time.Time, label string) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch preset {
	case "yesterday":
		curStart = today.AddDate(0, 0, -1)
		curEnd = today
		label = "Yesterday"
	case "this_week":
		wd := int(today.Weekday())
		if wd == 0 {
			wd = 7
		}
		curStart = today.AddDate(0, 0, -(wd - 1))
		curEnd = curStart.AddDate(0, 0, 7)
		label = "This week"
	case "last_week":
		wd := int(today.Weekday())
		if wd == 0 {
			wd = 7
		}
		curEnd = today.AddDate(0, 0, -(wd - 1))
		curStart = curEnd.AddDate(0, 0, -7)
		label = "Last week"
	case "this_month":
		curStart = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		curEnd = curStart.AddDate(0, 1, 0)
		label = "This month"
	case "last_month":
		curEnd = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		curStart = curEnd.AddDate(0, -1, 0)
		label = "Last month"
	case "this_year":
		curStart = time.Date(today.Year(), 1, 1, 0, 0, 0, 0, today.Location())
		curEnd = curStart.AddDate(1, 0, 0)
		label = "This year"
	case "custom":
		from, errF := time.Parse("2006-01-02", fromStr)
		to, errT := time.Parse("2006-01-02", toStr)
		if errF == nil && errT == nil && !to.Before(from) {
			curStart = from
			curEnd = to.AddDate(0, 0, 1)
			label = from.Format("Jan 2") + " – " + to.Format("Jan 2")
		} else {
			curStart = today
			curEnd = today.AddDate(0, 0, 1)
			label = "Today"
		}
	default: // today
		curStart = today
		curEnd = today.AddDate(0, 0, 1)
		label = "Today"
	}
	// Previous period of equal length.
	dur := curEnd.Sub(curStart)
	prevEnd = curStart
	prevStart = prevEnd.Add(-dur)
	return
}

func pctDelta(cur, prev float64) (float64, bool) {
	if prev == 0 {
		if cur == 0 {
			return 0, false
		}
		return 100, true
	}
	return (cur - prev) / prev * 100, true
}

// metricsFor returns a bag of raw numbers for the given period.
type metricsBag struct {
	Reservations        float64
	WebsiteReservations float64
	Cancelled           float64
	WithExtras          float64
	Revenue             float64
	Paid                float64
	Outstanding         float64
	ReservationValueSum float64
	ReservationValueN   float64
	RentalDaysSum       float64
	RentalDaysN         float64
	DailyRateSum        float64
	DailyRateN          float64
}

func (h *Handler) metricsFor(ctx context.Context, cid uuid.UUID, start, end time.Time) metricsBag {
	var m metricsBag
	// Reservation-side aggregates (counts by pickup_at falling in range).
	_ = h.Pool.QueryRow(ctx, `
		SELECT
		  COUNT(*)::float,
		  COUNT(*) FILTER (WHERE source='customer')::float,
		  COUNT(*) FILTER (WHERE status='cancelled')::float,
		  COUNT(*) FILTER (WHERE extras_cents > 0)::float,
		  COALESCE(SUM(total_cents) FILTER (WHERE status NOT IN ('cancelled','no_show')), 0)::float / 100.0,
		  COALESCE(SUM(GREATEST(total_cents - paid_cents, 0)) FILTER (WHERE status NOT IN ('cancelled','no_show')), 0)::float / 100.0,
		  COALESCE(COUNT(*) FILTER (WHERE status NOT IN ('cancelled','no_show')), 0)::float,
		  COALESCE(SUM(rental_days) FILTER (WHERE status NOT IN ('cancelled','no_show')), 0)::float,
		  COALESCE(SUM(daily_rate_cents) FILTER (WHERE status NOT IN ('cancelled','no_show')), 0)::float / 100.0
		FROM reservations
		WHERE company_id=$1 AND pickup_at >= $2 AND pickup_at < $3
	`, cid, start, end).Scan(
		&m.Reservations, &m.WebsiteReservations, &m.Cancelled, &m.WithExtras,
		&m.Revenue, &m.Outstanding,
		&m.ReservationValueN, // same N used for value, days, and daily rate avgs
		&m.RentalDaysSum,
		&m.DailyRateSum,
	)
	m.ReservationValueSum = m.Revenue
	m.RentalDaysN = m.ReservationValueN
	m.DailyRateN = m.ReservationValueN
	// Collected payments in range.
	_ = h.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_cents),0)::float / 100.0
		FROM payments WHERE company_id=$1 AND status='succeeded' AND created_at >= $2 AND created_at < $3
	`, cid, start, end).Scan(&m.Paid)
	return m
}

func (h *Handler) fleetSize(ctx context.Context, cid uuid.UUID) int {
	var n int
	_ = h.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM vehicles WHERE company_id=$1`, cid).Scan(&n)
	return n
}

type FleetStatusCount struct {
	Status string
	Count  int
}

func (h *Handler) fleetBreakdown(ctx context.Context, cid uuid.UUID) []FleetStatusCount {
	rows, err := h.Pool.Query(ctx, `SELECT status, COUNT(*) FROM vehicles WHERE company_id=$1 GROUP BY status`, cid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []FleetStatusCount{}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		out = append(out, FleetStatusCount{s, n})
	}
	return out
}

func (h *Handler) bookingsPerDay(ctx context.Context, cid uuid.UUID, start, end time.Time) []ChartPoint {
	rows, err := h.Pool.Query(ctx, `
		SELECT d::date, COALESCE(c.cnt, 0)
		FROM generate_series($1::date, ($2::date - interval '1 day')::date, interval '1 day') d
		LEFT JOIN (
		  SELECT pickup_at::date AS day, COUNT(*) AS cnt
		  FROM reservations WHERE company_id=$3 AND pickup_at >= $1 AND pickup_at < $2
		  GROUP BY pickup_at::date
		) c ON c.day = d::date
		ORDER BY d
	`, start, end, cid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ChartPoint{}
	for rows.Next() {
		var d time.Time
		var n float64
		_ = rows.Scan(&d, &n)
		out = append(out, ChartPoint{Label: d.Format("Jan 2"), Value: n})
	}
	return out
}

func (h *Handler) bookingsByStatus(ctx context.Context, cid uuid.UUID, start, end time.Time) []ChartBar {
	rows, err := h.Pool.Query(ctx, `
		SELECT status, COUNT(*) FROM reservations
		WHERE company_id=$1 AND pickup_at >= $2 AND pickup_at < $3
		GROUP BY status ORDER BY COUNT(*) DESC
	`, cid, start, end)
	if err != nil {
		return nil
	}
	defer rows.Close()
	colors := map[string]string{
		"pending":          "#d97706",
		"confirmed":        "#0f3d3e",
		"checked_out":      "#1e40af",
		"returned":         "#15803d",
		"cancelled":        "#b91c1c",
		"no_show":          "#525252",
		"return_overdue":   "#dc2626",
		"awaiting_pickup":  "#7c3aed",
	}
	out := []ChartBar{}
	for rows.Next() {
		var s string
		var n float64
		_ = rows.Scan(&s, &n)
		c, ok := colors[s]
		if !ok {
			c = "#9ca3af"
		}
		out = append(out, ChartBar{Label: s, Value: n, Color: c})
	}
	return out
}

func (h *Handler) revenuePerDay(ctx context.Context, cid uuid.UUID, start, end time.Time) []ChartPoint {
	rows, err := h.Pool.Query(ctx, `
		SELECT d::date, COALESCE(c.sum_cents, 0)::float / 100.0
		FROM generate_series($1::date, ($2::date - interval '1 day')::date, interval '1 day') d
		LEFT JOIN (
		  SELECT created_at::date AS day, SUM(amount_cents) AS sum_cents
		  FROM payments WHERE company_id=$3 AND status='succeeded' AND created_at >= $1 AND created_at < $2
		  GROUP BY created_at::date
		) c ON c.day = d::date
		ORDER BY d
	`, start, end, cid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []ChartPoint{}
	for rows.Next() {
		var d time.Time
		var v float64
		_ = rows.Scan(&d, &v)
		out = append(out, ChartPoint{Label: d.Format("Jan 2"), Value: v})
	}
	return out
}

// buildKPIs assembles the 11 cards with prev-period deltas.
func (h *Handler) buildKPIs(cur, prev metricsBag, fleet int) []KPI {
	fleetF := float64(fleet)
	k := []KPI{}

	add := func(label, hint string, cur, prev float64, isMoney, invertColor bool) {
		val := ""
		if isMoney {
			val = fmt.Sprintf("$%s", humanMoney(cur))
		} else if math.Floor(cur) == cur {
			val = fmt.Sprintf("%.0f", cur)
		} else {
			val = fmt.Sprintf("%.2f", cur)
		}
		d, hasD := pctDelta(cur, prev)
		positive := d >= 0
		if invertColor {
			positive = !positive
		}
		k = append(k, KPI{Label: label, Value: val, Raw: cur, Delta: d, HasDelta: hasD, Positive: positive, Hint: hint})
	}

	add("Total revenue", "sum of reservation totals in period, excl. cancelled", cur.Revenue, prev.Revenue, true, false)
	add("Amount paid", "collected payments in period", cur.Paid, prev.Paid, true, false)
	add("Outstanding balance", "unpaid share of active reservations", cur.Outstanding, prev.Outstanding, true, true)
	avgCur := safeDiv(cur.ReservationValueSum, cur.ReservationValueN)
	avgPrev := safeDiv(prev.ReservationValueSum, prev.ReservationValueN)
	add("Avg reservation value", "mean non-cancelled booking total", avgCur, avgPrev, true, false)
	adrCur := safeDiv(cur.DailyRateSum, cur.DailyRateN)
	adrPrev := safeDiv(prev.DailyRateSum, prev.DailyRateN)
	add("Avg daily rate", "mean daily_rate across non-cancelled bookings", adrCur, adrPrev, true, false)
	rpvCur := safeDiv(cur.Revenue, fleetF)
	rpvPrev := safeDiv(prev.Revenue, fleetF)
	add("Revenue per vehicle", "revenue / fleet size", rpvCur, rpvPrev, true, false)

	add("Total reservations", "all bookings whose pickup falls in period", cur.Reservations, prev.Reservations, false, false)
	add("Website reservations", "bookings created by the customer site", cur.WebsiteReservations, prev.WebsiteReservations, false, false)
	avgDays := safeDiv(cur.RentalDaysSum, cur.RentalDaysN)
	avgDaysP := safeDiv(prev.RentalDaysSum, prev.RentalDaysN)
	add("Avg rental duration", "mean rental_days across non-cancelled bookings", avgDays, avgDaysP, false, false)
	add("Bookings with extras", "bookings carrying add-on charges", cur.WithExtras, prev.WithExtras, false, false)
	ratioCur := safeDiv(cur.Reservations, fleetF)
	ratioPrev := safeDiv(prev.Reservations, fleetF)
	add("Reservations / fleet", "bookings per vehicle in period", ratioCur, ratioPrev, false, false)

	return k
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func humanMoney(v float64) string {
	// Format with thousands separator.
	neg := v < 0
	if neg {
		v = -v
	}
	intPart := int64(math.Floor(v))
	frac := int64(math.Round((v - math.Floor(v)) * 100))
	// Insert commas
	s := fmt.Sprintf("%d", intPart)
	out := ""
	for i, r := range reverseStr(s) {
		if i > 0 && i%3 == 0 {
			out = "," + out
		}
		out = string(r) + out
	}
	// Hm — my accumulator logic is reversed. Reset to simpler:
	out = commas(intPart)
	s = fmt.Sprintf("%s.%02d", out, frac)
	if neg {
		s = "-" + s
	}
	return s
}
func reverseStr(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := ""
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(r)
	}
	return out
}

// =============================================================================
// HTTP handler
// =============================================================================

func (h *Handler) DashboardV2(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "overview"
	}

	data := map[string]any{
		"View": view,
		"From": r.URL.Query().Get("from"),
		"To":   r.URL.Query().Get("to"),
	}

	switch view {
	case "overview":
		h.loadOverview(r, cid, data)
	case "daily-manifest":
		h.loadDailyManifest(r, cid, data)
	case "getting-started":
		h.loadGettingStarted(r, cid, data)
	case "bookings":
		h.loadBookingsPanel(r, cid, data)
	case "availability":
		h.loadAvailabilityPanel(r, cid, data)
	case "fleet":
		h.loadFleetPanel(r, cid, data)
	case "extras":
		h.loadExtrasPanel(r, cid, data)
	case "attempts", "commissions":
		// handled by template as empty states
	default:
		view = "overview"
		data["View"] = view
		h.loadOverview(r, cid, data)
	}

	h.render(w, r, "admin/dashboard_v2.html", "Dashboard", "dashboard", data)
}

func (h *Handler) loadOverview(r *http.Request, cid uuid.UUID, data map[string]any) {
	preset := r.URL.Query().Get("preset")
	if preset == "" {
		preset = "this_month"
	}
	curStart, curEnd, prevStart, prevEnd, rangeLabel := resolveRange(preset, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	cur := h.metricsFor(r.Context(), cid, curStart, curEnd)
	prev := h.metricsFor(r.Context(), cid, prevStart, prevEnd)
	fleet := h.fleetSize(r.Context(), cid)
	data["KPIs"] = h.buildKPIs(cur, prev, fleet)
	data["Preset"] = preset
	data["RangeLabel"] = rangeLabel
	data["CurStart"] = curStart
	data["CurEnd"] = curEnd.AddDate(0, 0, -1)
	data["BookingsPerDay"] = h.bookingsPerDay(r.Context(), cid, curStart, curEnd)
	data["BookingsByStatus"] = h.bookingsByStatus(r.Context(), cid, curStart, curEnd)
	data["RevenuePerDay"] = h.revenuePerDay(r.Context(), cid, curStart, curEnd)
	data["FleetBreakdown"] = h.fleetBreakdown(r.Context(), cid)
	data["FleetSize"] = fleet

	// Today-snapshot counters.
	today := time.Now().Truncate(24 * time.Hour)
	tomorrow := today.AddDate(0, 0, 1)
	var tp, tr, tmp, tmr, ar, ov int
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND pickup_at::date=$2::date`, cid, today).Scan(&tp)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND return_at::date=$2::date`, cid, today).Scan(&tr)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND pickup_at::date=$2::date`, cid, tomorrow).Scan(&tmp)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND return_at::date=$2::date`, cid, tomorrow).Scan(&tmr)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='checked_out'`, cid).Scan(&ar)
	_ = h.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM reservations WHERE company_id=$1 AND status='checked_out' AND return_at < NOW()`, cid).Scan(&ov)
	data["TodayPickups"] = tp
	data["TodayReturns"] = tr
	data["TomorrowPickups"] = tmp
	data["TomorrowReturns"] = tmr
	data["ActiveRentals"] = ar
	data["OverdueRentals"] = ov
}
