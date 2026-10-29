package web

import (
	"html/template"
	"strings"
)

// Lucide-style inline SVG icons used in the admin sidebar + topbar.
// Kept small, mono-stroked, so they inherit color from CSS.
var icons = map[string]string{
	"dashboard": `<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>`,
	"people":    `<path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M22 21v-2a4 4 0 0 0-3-3.87"/><path d="M16 3.13a4 4 0 0 1 0 7.75"/>`,
	"calendar":  `<rect x="3" y="4" width="18" height="18" rx="2"/><path d="M16 2v4M8 2v4M3 10h18"/>`,
	"clipboard": `<rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="M8 12h8M8 16h6"/>`,
	"chat":      `<path d="M21 11.5a8.38 8.38 0 0 1-.9 3.8 8.5 8.5 0 0 1-7.6 4.7 8.38 8.38 0 0 1-3.8-.9L3 21l1.9-5.7a8.38 8.38 0 0 1-.9-3.8 8.5 8.5 0 0 1 4.7-7.6 8.38 8.38 0 0 1 3.8-.9h.5a8.48 8.48 0 0 1 8 8v.5z"/>`,
	"doc":       `<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><path d="M9 13h6M9 17h6"/>`,
	"car":       `<path d="M5 17h14a1 1 0 0 0 1-1v-4a1 1 0 0 0-.3-.7l-2.1-2.1A2 2 0 0 0 16 8.5H8a2 2 0 0 0-1.4.6L4.3 11.3A1 1 0 0 0 4 12v4a1 1 0 0 0 1 1z"/><circle cx="7.5" cy="17.5" r="1.5"/><circle cx="16.5" cy="17.5" r="1.5"/>`,
	"wrench":    `<path d="M14.7 6.3a4 4 0 1 0 5 5l-3-1.5-1.5-3zm-1.4 1.4L5.5 15.5a2 2 0 1 0 2.8 2.8l7.8-7.8"/>`,
	"user":      `<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>`,
	"id":        `<rect x="3" y="4" width="18" height="16" rx="2"/><circle cx="9" cy="12" r="2.5"/><path d="M14 10h4M14 14h4M4 18c0-2 2-3 5-3s5 1 5 3"/>`,
	"chart":     `<path d="M3 3v18h18"/><path d="M7 15l4-4 3 3 5-6"/>`,
	"gear":      `<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09a1.65 1.65 0 0 0-1-1.51 1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09a1.65 1.65 0 0 0 1.51-1 1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33h.01a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82v.01a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>`,
}

func sideIcon(name string) template.HTML {
	body, ok := icons[name]
	if !ok {
		body = icons["dashboard"]
	}
	return template.HTML(`<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`)
}

func initials2(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	s := string(parts[0][0])
	if len(parts) > 1 {
		s += string(parts[len(parts)-1][0])
	}
	return strings.ToUpper(s)
}
