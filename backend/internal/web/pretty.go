package web

import "strings"

// Human-readable labels for common vehicle feature codes. Keys match the
// snake_case tokens stored in vehicles.features. Unknown keys fall through
// to a Title Case conversion of the raw snake_case string.
var featurePretty = map[string]string{
	"ac":                 "Air Conditioning",
	"air_conditioning":   "Air Conditioning",
	"auto_ac":            "Automatic A/C",
	"heated_seats":       "Heated Seats",
	"ventilated_seats":   "Ventilated Seats",
	"leather_seats":      "Leather Seats",
	"sunroof":            "Sunroof",
	"moonroof":           "Moonroof",
	"panoramic_roof":     "Panoramic Roof",
	"bluetooth":          "Bluetooth",
	"android_auto":       "Android Auto",
	"apple_carplay":      "Apple CarPlay",
	"carplay":            "Apple CarPlay",
	"usb_ports":          "USB Ports",
	"wireless_charging":  "Wireless Charging",
	"backup_camera":      "Backup Camera",
	"parking_sensors":    "Parking Sensors",
	"360_camera":         "360° Camera",
	"blind_spot":         "Blind Spot Monitor",
	"lane_assist":        "Lane Keep Assist",
	"adaptive_cruise":    "Adaptive Cruise Control",
	"cruise_control":     "Cruise Control",
	"automatic":          "Automatic Transmission",
	"manual":             "Manual Transmission",
	"awd":                "All-Wheel Drive",
	"4wd":                "4-Wheel Drive",
	"fwd":                "Front-Wheel Drive",
	"rwd":                "Rear-Wheel Drive",
	"navigation":         "Navigation System",
	"gps":                "GPS Navigation",
	"keyless_entry":      "Keyless Entry",
	"push_button_start":  "Push-Button Start",
	"remote_start":       "Remote Start",
	"roof_rack":          "Roof Rack",
	"tow_hitch":          "Tow Hitch",
	"child_seat":         "Child Seat",
	"booster_seat":       "Booster Seat",
	"spare_tire":         "Spare Tire",
	"first_aid":          "First Aid Kit",
	"wifi":               "In-Car Wi-Fi",
	"premium_audio":      "Premium Audio",
	"subwoofer":          "Subwoofer",
	"heated_steering":    "Heated Steering Wheel",
	"power_seats":        "Power-Adjustable Seats",
	"memory_seats":       "Memory Seats",
	"third_row":          "Third-Row Seating",
	"sliding_doors":      "Sliding Doors",
	"power_tailgate":     "Power Tailgate",
	"tinted_windows":     "Tinted Windows",
	"fog_lights":         "Fog Lights",
	"led_headlights":     "LED Headlights",
	"xenon_headlights":   "Xenon Headlights",
	"alloy_wheels":       "Alloy Wheels",
}

// prettyFeature turns "parking_sensors" into "Parking Sensors" using the
// known map above, falling back to Title Case for unknown codes.
func prettyFeature(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	if v, ok := featurePretty[strings.ToLower(code)]; ok {
		return v
	}
	// Fallback: snake_case / kebab-case -> Title Case.
	norm := strings.NewReplacer("_", " ", "-", " ").Replace(code)
	words := strings.Fields(norm)
	for i, w := range words {
		if len(w) == 0 {
			continue
		}
		// Preserve all-caps acronyms (USB, GPS, LED, etc.).
		if strings.ToUpper(w) == w && len(w) <= 4 {
			words[i] = w
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
	}
	return strings.Join(words, " ")
}
