// Package meeting turns calendar invitations (iCalendar, RFC 5545) and bare
// video-call links into core.Meeting values. The parser is deliberately
// small: it reads what a meeting list needs (summary, times, organizer,
// join link, recurrence) and ignores the rest of the format.
package meeting

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	// Zone data travels with the binary: the daemon may run in a
	// container without /usr/share/zoneinfo, and an invitation's wall
	// clock is useless without its zone.
	_ "time/tzdata"

	"github.com/reyer3/bunker-go/internal/core"
)

// prop is one content line: NAME;PARAM=v:value.
type prop struct {
	name   string
	params map[string]string
	value  string
}

// component is a BEGIN/END block with its properties and sub-blocks.
type component struct {
	name     string
	props    []prop
	children []*component
}

func (c *component) first(name string) (prop, bool) {
	for _, p := range c.props {
		if p.name == name {
			return p, true
		}
	}
	return prop{}, false
}

func (c *component) text(name string) string {
	p, _ := c.first(name)
	return unescapeText(p.value)
}

// unfold joins continuation lines (a line starting with a space or tab
// continues the previous one) and splits on CRLF or bare LF.
func unfold(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	var lines []string
	for _, raw := range strings.Split(data, "\n") {
		if raw == "" {
			continue
		}
		if (raw[0] == ' ' || raw[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += raw[1:]
			continue
		}
		lines = append(lines, raw)
	}
	return lines
}

// parseLine splits a content line into name, parameters and value. The
// first colon outside double quotes ends the name and parameters, since a
// quoted parameter value (CN="Doe: Ana") may contain one.
func parseLine(line string) (prop, bool) {
	inQuote := false
	colon := -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case ':':
			if !inQuote {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}
	if colon < 0 {
		return prop{}, false
	}
	head, value := line[:colon], line[colon+1:]
	parts := splitOutsideQuotes(head, ';')
	p := prop{name: strings.ToUpper(strings.TrimSpace(parts[0])), value: value}
	for _, part := range parts[1:] {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if p.params == nil {
			p.params = map[string]string{}
		}
		p.params[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(v, `"`)
	}
	return p, true
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// unescapeText undoes TEXT escaping: \n \N newline, \, \; \\.
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseComponents builds the component tree of data.
func parseComponents(data string) ([]*component, error) {
	var roots []*component
	var stack []*component
	for _, line := range unfold(data) {
		p, ok := parseLine(line)
		if !ok {
			continue
		}
		switch p.name {
		case "BEGIN":
			c := &component{name: strings.ToUpper(strings.TrimSpace(p.value))}
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.children = append(top.children, c)
			} else {
				roots = append(roots, c)
			}
			stack = append(stack, c)
		case "END":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.props = append(top.props, p)
			}
		}
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("meeting: no calendar in data")
	}
	return roots, nil
}

// ParseICS extracts the meetings of an iCalendar document: one per
// VEVENT of a REQUEST, PUBLISH or CANCEL (a calendar without METHOD counts
// as PUBLISH). REPLY, COUNTER and the other methods are answers between
// attendees, not invitations, and yield nothing. An event without
// DTSTART is skipped; one whose times cannot be read is an error, so a
// malformed invitation is reported instead of silently missing.
func ParseICS(data string) ([]core.Meeting, error) {
	roots, err := parseComponents(data)
	if err != nil {
		return nil, err
	}
	var out []core.Meeting
	for _, cal := range roots {
		if cal.name != "VCALENDAR" {
			continue
		}
		method := strings.ToUpper(strings.TrimSpace(cal.text("METHOD")))
		switch method {
		case "":
			method = "PUBLISH"
		case "REQUEST", "PUBLISH", "CANCEL":
		default:
			continue
		}
		zones := vtimezones(cal)
		for _, ev := range cal.children {
			if ev.name != "VEVENT" {
				continue
			}
			m, ok, err := eventMeeting(ev, method, zones)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func eventMeeting(ev *component, method string, zones map[string]*component) (core.Meeting, bool, error) {
	startProp, ok := ev.first("DTSTART")
	if !ok {
		return core.Meeting{}, false, nil
	}
	start, loc, allDay, err := parseTime(startProp, zones)
	if err != nil {
		return core.Meeting{}, false, fmt.Errorf("meeting: DTSTART: %w", err)
	}
	var end time.Time
	if endProp, ok := ev.first("DTEND"); ok {
		end, _, _, err = parseTime(endProp, zones)
		if err != nil {
			return core.Meeting{}, false, fmt.Errorf("meeting: DTEND: %w", err)
		}
	} else if durProp, ok := ev.first("DURATION"); ok {
		d, err := parseDuration(durProp.value)
		if err != nil {
			return core.Meeting{}, false, fmt.Errorf("meeting: DURATION: %w", err)
		}
		end = start.Add(d)
	} else if allDay {
		end = start.AddDate(0, 0, 1)
	} else {
		end = start
	}
	if end.Before(start) {
		end = start
	}

	m := core.Meeting{
		UID:       strings.TrimSpace(ev.text("UID")),
		Method:    method,
		Summary:   strings.TrimSpace(ev.text("SUMMARY")),
		Start:     start,
		End:       end,
		AllDay:    allDay,
		Location:  strings.TrimSpace(ev.text("LOCATION")),
		Organizer: organizer(ev),
		Cancelled: method == "CANCEL" || strings.EqualFold(strings.TrimSpace(ev.text("STATUS")), "CANCELLED"),
	}
	if seq, err := strconv.Atoi(strings.TrimSpace(ev.text("SEQUENCE"))); err == nil {
		m.Sequence = seq
	}
	if loc != nil && loc != time.UTC && loc != time.Local {
		m.TZ = loc.String()
	}
	if rid, ok := ev.first("RECURRENCE-ID"); ok {
		// One instance of a series: its own meeting, not the series.
		m.UID += "#" + rid.value
	} else if rule := strings.TrimSpace(ev.text("RRULE")); rule != "" {
		m.Rule = rule
		m.ExDates = exDates(ev, zones)
	}
	m.URL = joinURL(ev)
	return m, true, nil
}

func organizer(ev *component) string {
	p, ok := ev.first("ORGANIZER")
	if !ok {
		return ""
	}
	addr := strings.TrimSpace(p.value)
	if len(addr) >= 7 && strings.EqualFold(addr[:7], "mailto:") {
		addr = addr[7:]
	}
	if cn := strings.TrimSpace(p.params["CN"]); cn != "" {
		if addr == "" {
			return cn
		}
		return cn + " <" + addr + ">"
	}
	return addr
}

func exDates(ev *component, zones map[string]*component) []time.Time {
	var out []time.Time
	for _, p := range ev.props {
		if p.name != "EXDATE" {
			continue
		}
		for _, v := range strings.Split(p.value, ",") {
			one := p
			one.value = strings.TrimSpace(v)
			if t, _, _, err := parseTime(one, zones); err == nil {
				out = append(out, t)
			}
		}
	}
	return out
}

// joinURL finds the call link of an event: the conferencing properties
// first (Google's and Microsoft's say outright where the call is), then
// any recognised provider link in the location, URL, description and the
// remaining X- properties. A location that is itself a web link is the
// last resort (self-hosted calls).
func joinURL(ev *component) string {
	for _, name := range []string{"X-GOOGLE-CONFERENCE", "X-MICROSOFT-SKYPETEAMSMEETINGURL", "X-MICROSOFT-ONLINEMEETINGCONFLINK"} {
		if p, ok := ev.first(name); ok {
			if v := strings.TrimSpace(unescapeText(p.value)); isWebURL(v) {
				return v
			}
		}
	}
	texts := []string{ev.text("LOCATION"), ev.text("URL"), ev.text("DESCRIPTION")}
	for _, p := range ev.props {
		if strings.HasPrefix(p.name, "X-") {
			texts = append(texts, unescapeText(p.value))
		}
	}
	if u := core.FindMeetingURL(texts...); u != "" {
		return u
	}
	if loc := strings.TrimSpace(ev.text("LOCATION")); isWebURL(loc) {
		return loc
	}
	return ""
}

func isWebURL(s string) bool {
	return !strings.ContainsAny(s, " \t\n") &&
		(strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://"))
}

// parseTime reads a DATE or DATE-TIME value. It returns the zone the
// value was written in (UTC for "Z", the TZID zone, else the machine's
// local zone for a floating time) and whether it was a DATE (all day).
func parseTime(p prop, zones map[string]*component) (t time.Time, loc *time.Location, allDay bool, err error) {
	v := strings.TrimSpace(p.value)
	if strings.EqualFold(p.params["VALUE"], "DATE") || (len(v) == 8 && !strings.Contains(v, "T")) {
		loc = zoneFor(p.params["TZID"], zones, time.Local)
		t, err = time.ParseInLocation("20060102", v, loc)
		if err != nil {
			return time.Time{}, nil, false, fmt.Errorf("bad date %q", v)
		}
		return t, loc, true, nil
	}
	if strings.HasSuffix(v, "Z") || strings.HasSuffix(v, "z") {
		t, err = time.Parse("20060102T150405Z", strings.ToUpper(v))
		if err != nil {
			return time.Time{}, nil, false, fmt.Errorf("bad UTC time %q", v)
		}
		return t, time.UTC, false, nil
	}
	loc = zoneFor(p.params["TZID"], zones, time.Local)
	t, err = time.ParseInLocation("20060102T150405", v, loc)
	if err != nil {
		return time.Time{}, nil, false, fmt.Errorf("bad time %q", v)
	}
	return t, loc, false, nil
}

// parseDuration reads an RFC 5545 duration (P1D, PT1H30M, P1W, -PT15M).
func parseDuration(v string) (time.Duration, error) {
	s := strings.TrimSpace(v)
	sign := time.Duration(1)
	if strings.HasPrefix(s, "-") {
		sign, s = -1, s[1:]
	} else {
		s = strings.TrimPrefix(s, "+")
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("bad duration %q", v)
	}
	s = s[1:]
	var total time.Duration
	num := ""
	inTime := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'T':
			inTime = true
		default:
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, fmt.Errorf("bad duration %q", v)
			}
			num = ""
			switch {
			case r == 'W':
				total += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				total += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				total += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				total += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				total += time.Duration(n) * time.Second
			default:
				return 0, fmt.Errorf("bad duration %q", v)
			}
		}
	}
	if num != "" {
		return 0, fmt.Errorf("bad duration %q", v)
	}
	return sign * total, nil
}
