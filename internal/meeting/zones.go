package meeting

import (
	"strings"
	"time"
)

// windowsZones maps the Windows zone names Outlook writes in TZID to IANA
// names. It covers the common ones; anything else falls back to the
// invitation's own VTIMEZONE.
var windowsZones = map[string]string{
	"UTC":                             "UTC",
	"Pacific Standard Time":           "America/Los_Angeles",
	"Mountain Standard Time":          "America/Denver",
	"US Mountain Standard Time":       "America/Phoenix",
	"Central Standard Time":           "America/Chicago",
	"Central Standard Time (Mexico)":  "America/Mexico_City",
	"Eastern Standard Time":           "America/New_York",
	"SA Pacific Standard Time":        "America/Bogota",
	"SA Western Standard Time":        "America/La_Paz",
	"SA Eastern Standard Time":        "America/Cayenne",
	"Pacific SA Standard Time":        "America/Santiago",
	"Argentina Standard Time":         "America/Argentina/Buenos_Aires",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Atlantic Standard Time":          "America/Halifax",
	"Venezuela Standard Time":         "America/Caracas",
	"Greenwich Standard Time":         "Atlantic/Reykjavik",
	"GMT Standard Time":               "Europe/London",
	"W. Europe Standard Time":         "Europe/Berlin",
	"Romance Standard Time":           "Europe/Paris",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Central European Standard Time":  "Europe/Warsaw",
	"E. Europe Standard Time":         "Europe/Chisinau",
	"GTB Standard Time":               "Europe/Bucharest",
	"FLE Standard Time":               "Europe/Kiev",
	"Russian Standard Time":           "Europe/Moscow",
	"Turkey Standard Time":            "Europe/Istanbul",
	"Israel Standard Time":            "Asia/Jerusalem",
	"Arab Standard Time":              "Asia/Riyadh",
	"Arabian Standard Time":           "Asia/Dubai",
	"India Standard Time":             "Asia/Kolkata",
	"China Standard Time":             "Asia/Shanghai",
	"Singapore Standard Time":         "Asia/Singapore",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Korea Standard Time":             "Asia/Seoul",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"New Zealand Standard Time":       "Pacific/Auckland",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"Egypt Standard Time":             "Africa/Cairo",
	"W. Central Africa Standard Time": "Africa/Lagos",
}

// vtimezones indexes a calendar's VTIMEZONE blocks by TZID.
func vtimezones(cal *component) map[string]*component {
	zones := map[string]*component{}
	for _, c := range cal.children {
		if c.name == "VTIMEZONE" {
			if id := strings.TrimSpace(c.text("TZID")); id != "" {
				zones[id] = c
			}
		}
	}
	return zones
}

// zoneFor resolves a TZID: an IANA name, a Windows name, or the
// invitation's VTIMEZONE; with no TZID (a floating time) it is fallback.
// A TZID nothing resolves also gets fallback, the machine's zone, since
// the invitation was written for the person running it.
func zoneFor(tzid string, zones map[string]*component, fallback *time.Location) *time.Location {
	tzid = strings.TrimSpace(strings.Trim(tzid, `"`))
	if tzid == "" {
		return fallback
	}
	// A leading "/" marks a globally unique id ("/mozilla.org/.../Europe/Madrid").
	if i := strings.Index(tzid, "/"); i == 0 {
		if parts := strings.Split(tzid, "/"); len(parts) >= 3 {
			tzid = strings.Join(parts[len(parts)-2:], "/")
		}
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if name, ok := windowsZones[tzid]; ok {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if z, ok := zones[tzid]; ok {
		if loc := vtimezoneLocation(tzid, z); loc != nil {
			return loc
		}
	}
	return fallback
}

// vtimezoneLocation builds a fixed-offset zone from a VTIMEZONE: the
// STANDARD offset, or DAYLIGHT's when only that exists. It ignores the
// daylight-saving rules, so it is only the last resort after IANA and
// Windows names.
func vtimezoneLocation(name string, z *component) *time.Location {
	offset := func(c *component) (int, bool) {
		v := strings.TrimSpace(c.text("TZOFFSETTO"))
		if len(v) < 5 {
			return 0, false
		}
		sign := 1
		if v[0] == '-' {
			sign = -1
		}
		v = strings.TrimLeft(v, "+-")
		if len(v) < 4 {
			return 0, false
		}
		h, m := atoi2(v[0:2]), atoi2(v[2:4])
		return sign * (h*3600 + m*60), true
	}
	var daylight *int
	for _, c := range z.children {
		off, ok := offset(c)
		if !ok {
			continue
		}
		if c.name == "STANDARD" {
			return time.FixedZone(name, off)
		}
		if c.name == "DAYLIGHT" && daylight == nil {
			o := off
			daylight = &o
		}
	}
	if daylight != nil {
		return time.FixedZone(name, *daylight)
	}
	return nil
}

func atoi2(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
