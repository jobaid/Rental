package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type TaxSettings struct {
	Label     string `json:"label"`
	RatePct   int    `json:"ratePct"`
	Inclusive bool   `json:"inclusive"`
}

// taxFor returns the company's tax settings (defaults to 0% / "Tax" / exclusive).
func (h *Handler) taxFor(ctx context.Context, cid uuid.UUID) TaxSettings {
	t := TaxSettings{Label: "Tax", RatePct: 0, Inclusive: false}
	var raw []byte
	if err := h.Pool.QueryRow(ctx, `SELECT value FROM settings WHERE company_id=$1 AND key='tax'`, cid).Scan(&raw); err == nil {
		var m map[string]any
		if err := jsonUnmarshal(raw, &m); err == nil {
			if v, ok := m["label"].(string); ok && v != "" { t.Label = v }
			if v, ok := m["ratePct"].(float64); ok { t.RatePct = int(v) }
			if v, ok := m["inclusive"].(bool); ok { t.Inclusive = v }
		}
	}
	return t
}

// rateForVehicle returns the daily rate (in cents) that applies to the given pickup date.
// It picks the vehicle_rate_periods row where [starts_on, ends_on] covers pickup. If none, falls back to vehicles.daily_rate_cents.
func (h *Handler) rateForVehicle(ctx context.Context, cid, vehicleID uuid.UUID, pickup time.Time) (int, error) {
	var rate int
	err := h.Pool.QueryRow(ctx, `
		SELECT daily_rate_cents FROM vehicle_rate_periods
		WHERE company_id=$1 AND vehicle_id=$2 AND is_active
		  AND $3::date BETWEEN starts_on AND ends_on
		ORDER BY (ends_on - starts_on) ASC
		LIMIT 1
	`, cid, vehicleID, pickup).Scan(&rate)
	if err == nil {
		return rate, nil
	}
	// Fall back to the vehicle default.
	err = h.Pool.QueryRow(ctx, `SELECT daily_rate_cents FROM vehicles WHERE company_id=$1 AND id=$2`, cid, vehicleID).Scan(&rate)
	return rate, err
}

// pricingBreakdown computes subtotal, tax and total for a given vehicle + date range,
// honoring vehicle_rate_periods and the company's tax setting.
type PricingBreakdown struct {
	DailyRateCents int
	Days           int
	SubtotalCents  int
	TaxCents       int
	TotalCents     int
	TaxLabel       string
	TaxRatePct     int
	TaxInclusive   bool
}

func (h *Handler) priceFor(ctx context.Context, cid, vehicleID uuid.UUID, pickup, ret time.Time) (PricingBreakdown, error) {
	rate, err := h.rateForVehicle(ctx, cid, vehicleID, pickup)
	if err != nil {
		return PricingBreakdown{}, err
	}
	days := rentalDays(pickup, ret)
	subtotal := rate * days
	tax := h.taxFor(ctx, cid)
	var taxCents, totalCents int
	if tax.Inclusive {
		// The quoted subtotal already includes tax; break it out for display.
		taxCents = subtotal - int(float64(subtotal)/(1+float64(tax.RatePct)/100))
		totalCents = subtotal
	} else {
		taxCents = subtotal * tax.RatePct / 100
		totalCents = subtotal + taxCents
	}
	return PricingBreakdown{
		DailyRateCents: rate, Days: days, SubtotalCents: subtotal,
		TaxCents: taxCents, TotalCents: totalCents,
		TaxLabel: tax.Label, TaxRatePct: tax.RatePct, TaxInclusive: tax.Inclusive,
	}, nil
}

// =============================================================================
// Vehicle rate period CRUD
// =============================================================================

type RatePeriod struct {
	ID             uuid.UUID
	StartsOn       time.Time
	EndsOn         time.Time
	DailyRateCents int
	Label          *string
	IsActive       bool
}

func (h *Handler) ratePeriodsFor(ctx context.Context, cid, vehicleID uuid.UUID) ([]RatePeriod, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT id, starts_on, ends_on, daily_rate_cents, label, is_active
		FROM vehicle_rate_periods WHERE company_id=$1 AND vehicle_id=$2
		ORDER BY starts_on
	`, cid, vehicleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RatePeriod{}
	for rows.Next() {
		var p RatePeriod
		if err := rows.Scan(&p.ID, &p.StartsOn, &p.EndsOn, &p.DailyRateCents, &p.Label, &p.IsActive); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (h *Handler) RatePeriodCreate(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")
	_ = r.ParseForm()
	starts := strings.TrimSpace(r.FormValue("startsOn"))
	ends := strings.TrimSpace(r.FormValue("endsOn"))
	rate, _ := strconv.Atoi(r.FormValue("dailyRateCents"))
	label := strings.TrimSpace(r.FormValue("label"))
	if starts == "" || ends == "" || rate <= 0 {
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err=Dates+and+rate+(cents)+required#rates", http.StatusSeeOther)
		return
	}
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO vehicle_rate_periods (company_id, vehicle_id, starts_on, ends_on, daily_rate_cents, label)
		VALUES ($1,$2::uuid,$3::date,$4::date,$5,NULLIF($6,''))
	`, cid, vid, starts, ends, rate, label)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "vrp_no_overlap") {
			msg = "That date range overlaps an existing period"
		}
		http.Redirect(w, r, "/admin/vehicles/"+vid+"?err="+msg+"#rates", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Rate+period+added#rates", http.StatusSeeOther)
}

func (h *Handler) RatePeriodDelete(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	vid := chi.URLParam(r, "id")
	rid := chi.URLParam(r, "rid")
	_, _ = h.Pool.Exec(r.Context(),
		`DELETE FROM vehicle_rate_periods WHERE company_id=$1 AND vehicle_id=$2 AND id=$3`, cid, vid, rid)
	http.Redirect(w, r, "/admin/vehicles/"+vid+"?flash=Rate+period+deleted#rates", http.StatusSeeOther)
}

// =============================================================================
// Tax settings
// =============================================================================

func (h *Handler) TaxPost(w http.ResponseWriter, r *http.Request) {
	s, _ := From(r)
	cid := *s.CompanyID
	_ = r.ParseForm()
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" { label = "Tax" }
	ratePct, _ := strconv.Atoi(r.FormValue("ratePct"))
	if ratePct < 0 { ratePct = 0 }
	if ratePct > 100 { ratePct = 100 }
	inclusive := r.FormValue("inclusive") == "on"

	cfg := map[string]any{"label": label, "ratePct": ratePct, "inclusive": inclusive}
	b, _ := jsonMarshal(cfg)
	_, err := h.Pool.Exec(r.Context(), `
		INSERT INTO settings (company_id, key, value) VALUES ($1,'tax',$2::jsonb)
		ON CONFLICT (company_id, key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()
	`, cid, string(b))
	if err != nil {
		http.Redirect(w, r, "/admin/settings?err="+err.Error()+"#tax", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/settings?flash=Tax+settings+saved#tax", http.StatusSeeOther)
}
