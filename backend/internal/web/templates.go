package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

//go:embed all:templates
var tmplFS embed.FS

//go:embed all:static
var StaticFS embed.FS

var funcs = template.FuncMap{
	"dollars":      func(cents int) string { return fmt.Sprintf("%.2f", float64(cents)/100.0) },
	"fmtTime":      func(t time.Time) string { return t.Format("Mon 02 Jan, 15:04") },
	"fmtDay":       func(t time.Time) string { return t.Format("Mon 1/2") },
	"sub":          func(a, b int) int { if r := a - b; r > 0 { return r }; return 0 },
	"add1":         func(a int) int { return a + 1 },
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 { return nil, fmt.Errorf("dict needs even args") }
		m := map[string]any{}
		for i := 0; i < len(values); i += 2 {
			k, ok := values[i].(string)
			if !ok { return nil, fmt.Errorf("dict keys must be strings") }
			m[k] = values[i+1]
		}
		return m, nil
	},
	"slotLabels": fleetSlotLabels,
	"initials":     initials,
	"statusClass":  statusClass,
	"or":           orStr,
	"cellFor":      cellFor,
	"barChart":  barChart,
	"hBarChart": hBarChart,
	"lineChart": lineChart,
	"sideIcon":  sideIcon,
	"initials2": initials2,
	"deref": func(p any) string {
		if p == nil {
			return ""
		}
		if s, ok := p.(*string); ok && s != nil {
			return *s
		}
		if s, ok := p.(string); ok {
			return s
		}
		return ""
	},
}

func initials(f, l *string) string {
	s := ""
	if f != nil && len(*f) > 0 { s += string((*f)[0]) }
	if l != nil && len(*l) > 0 { s += string((*l)[0]) }
	if s == "" { return "—" }
	return strings.ToUpper(s)
}

func statusClass(v any) string {
	s := ""
	switch t := v.(type) {
	case string:
		s = t
	case *string:
		if t != nil { s = *t }
	}
	switch s {
	case "confirmed", "returned", "checked_out": return "ok"
	case "cancelled", "no_show", "return_overdue": return "danger"
	case "pending", "inquiry": return "warn"
	default: return ""
	}
}

func orStr(a, b any) any {
	if a == nil { return b }
	switch v := a.(type) {
	case *string:
		if v == nil || *v == "" { return b }
		return *v
	case string:
		if v == "" { return b }
		return v
	case *int:
		if v == nil { return b }
		return *v
	}
	return a
}

type ReservationLite interface {
	Day() time.Time
}

// cellFor is wired in templates with specific types; we hand it any slice via reflection in the handler and pre-compute.
// To keep things simple we instead expect the handler to pre-group reservations and pass a 2-D map.
func cellFor(list []CalendarEvent, d time.Time) string {
	next := d.Add(24 * time.Hour)
	for _, r := range list {
		if r.PickupAt.Before(next) && r.ReturnAt.After(d) {
			switch r.Status {
			case "checked_out": return "OUT"
			case "returned":    return "ret"
			case "cancelled":   return ""
			default:            return "res"
			}
		}
	}
	return ""
}

type CalendarEvent struct {
	PickupAt time.Time
	ReturnAt time.Time
	Status   string
}

type Templates struct {
	store map[string]*template.Template
}

// Load parses each page template with its layout (public or admin) + base.
func Load() (*Templates, error) {
	t := &Templates{store: map[string]*template.Template{}}
	// map of pageName -> (pagePath, layoutPath)
	type page struct{ Name, PagePath, LayoutPath string }
	pages := []page{
		{"public/home.html",            "templates/public/home.html",            "templates/layouts/public.html"},
		{"public/search.html",          "templates/public/search.html",          "templates/layouts/public.html"},
		{"public/book.html",            "templates/public/book.html",            "templates/layouts/public.html"},
		{"public/confirm.html",         "templates/public/confirm.html",         "templates/layouts/public.html"},
		{"public/portal_login.html",    "templates/public/portal_login.html",    "templates/layouts/public.html"},
		{"public/portal.html",          "templates/public/portal.html",          "templates/layouts/public.html"},
		{"public/upload.html",          "templates/public/upload.html",          "templates/layouts/public.html"},
		{"public/about.html",           "templates/public/about.html",           "templates/layouts/public.html"},
		{"public/contact.html",         "templates/public/contact.html",         "templates/layouts/public.html"},
		{"public/faq.html",             "templates/public/faq.html",             "templates/layouts/public.html"},
		{"public/terms.html",           "templates/public/terms.html",           "templates/layouts/public.html"},
		{"public/legal.html",           "templates/public/legal.html",           "templates/layouts/public.html"},
		{"public/fleet_prep.html",      "templates/public/fleet_prep.html",      "templates/layouts/public.html"},

		{"admin/login.html",            "templates/admin/login.html",            ""},
		{"admin/setup.html",            "templates/admin/setup.html",            ""},
		{"admin/dashboard.html",        "templates/admin/dashboard.html",        "templates/layouts/admin.html"},
		{"admin/dashboard_v2.html",     "templates/admin/dashboard_v2.html",     "templates/layouts/admin.html"},
		{"admin/daily_manifest.html",   "templates/admin/daily_manifest.html",   "templates/layouts/admin.html"},
		{"admin/getting_started.html",  "templates/admin/getting_started.html",  "templates/layouts/admin.html"},
		{"admin/extras.html",           "templates/admin/extras.html",           "templates/layouts/admin.html"},
		{"admin/attempts.html",         "templates/admin/attempts.html",         "templates/layouts/admin.html"},
		{"admin/commissions.html",      "templates/admin/commissions.html",      "templates/layouts/admin.html"},
		{"admin/reservations.html",     "templates/admin/reservations.html",     "templates/layouts/admin.html"},
		{"admin/reservation_detail.html","templates/admin/reservation_detail.html","templates/layouts/admin.html"},
		{"admin/vehicles.html",         "templates/admin/vehicles.html",         "templates/layouts/admin.html"},
		{"admin/vehicle_new.html",      "templates/admin/vehicle_new.html",      "templates/layouts/admin.html"},
		{"admin/vehicle_detail.html",   "templates/admin/vehicle_detail.html",   "templates/layouts/admin.html"},
		{"admin/customers.html",        "templates/admin/customers.html",        "templates/layouts/admin.html"},
		{"admin/customer_detail.html",  "templates/admin/customer_detail.html",  "templates/layouts/admin.html"},
		{"admin/users.html",            "templates/admin/users.html",            "templates/layouts/admin.html"},
		{"admin/active.html",           "templates/admin/active.html",           "templates/layouts/admin.html"},
		{"admin/licenses.html",         "templates/admin/licenses.html",         "templates/layouts/admin.html"},
		{"admin/calendar.html",         "templates/admin/calendar.html",         "templates/layouts/admin.html"},
		{"admin/maintenance.html",      "templates/admin/maintenance.html",      "templates/layouts/admin.html"},
		{"admin/reports.html",          "templates/admin/reports.html",          "templates/layouts/admin.html"},
		{"admin/settings.html",         "templates/admin/settings.html",         "templates/layouts/admin.html"},
		{"admin/invoice_edit.html",     "templates/admin/invoice_edit.html",     "templates/layouts/admin.html"},
		{"admin/comms.html",            "templates/admin/comms.html",            "templates/layouts/admin.html"},
		{"admin/comms_templates.html",  "templates/admin/comms_templates.html",  "templates/layouts/admin.html"},
		{"admin/comms_rules.html",      "templates/admin/comms_rules.html",      "templates/layouts/admin.html"},
	}
	base := "templates/layouts/base.html"
	for _, p := range pages {
		files := []string{base}
		if p.LayoutPath != "" { files = append(files, p.LayoutPath) }
		files = append(files, p.PagePath)
		tp := template.New(filepath.Base(base)).Funcs(funcs)
		for _, f := range files {
			b, err := fs.ReadFile(tmplFS, f)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", f, err)
			}
			tp, err = tp.Parse(string(b))
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", f, err)
			}
		}
		t.store[p.Name] = tp
	}
	return t, nil
}

func (t *Templates) Render(name string, data any) (string, error) {
	// unused, keep for compat
	_ = t.store[name]
	return "", nil
}

func (t *Templates) Lookup(name string) *template.Template { return t.store[name] }
