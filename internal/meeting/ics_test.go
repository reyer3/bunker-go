package meeting

import (
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func ics(lines ...string) string { return strings.Join(lines, "\r\n") + "\r\n" }

func one(t *testing.T, data string) core.Meeting {
	t.Helper()
	ms, err := ParseICS(data)
	if err != nil {
		t.Fatalf("ParseICS: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("ParseICS = %d meetings, want 1", len(ms))
	}
	return ms[0]
}

func TestParseICSUTCWithFoldedLinesAndEscapes(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "METHOD:REQUEST", "BEGIN:VEVENT",
		"UID:abc-1@example.com",
		"SUMMARY:Revisión semanal\\, equipo A\\; fase",
		"DTSTART:20261005T143000Z", "DTEND:20261005T153000Z",
		"ORGANIZER;CN=\"Doe: Ana\":mailto:ana@example.com",
		"LOCATION:Sala 1",
		"DESCRIPTION:Hola\\nÚnete: https://meet.google.com/abc-defg-hij\\n",
		"SEQUENCE:2",
		"END:VEVENT", "END:VCALENDAR",
	))
	if m.UID != "abc-1@example.com" || m.Method != "REQUEST" || m.Sequence != 2 {
		t.Errorf("identity = %+v", m)
	}
	if m.Summary != "Revisión semanal, equipo A; fase" {
		t.Errorf("Summary = %q", m.Summary)
	}
	if want := time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC); !m.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", m.Start, want)
	}
	if m.End.Sub(m.Start) != time.Hour {
		t.Errorf("duration = %v", m.End.Sub(m.Start))
	}
	if m.Organizer != "Doe: Ana <ana@example.com>" {
		t.Errorf("Organizer = %q", m.Organizer)
	}
	if m.URL != "https://meet.google.com/abc-defg-hij" {
		t.Errorf("URL = %q", m.URL)
	}
}

func TestParseICSFoldedLine(t *testing.T) {
	m := one(t, "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:f1\nSUMMARY:Una reunión muy\n  larga que se\n\tplegó\nDTSTART:20261005T100000Z\nEND:VEVENT\nEND:VCALENDAR\n")
	if m.Summary != "Una reunión muy larga que se"+"plegó" {
		t.Errorf("Summary = %q", m.Summary)
	}
	if m.Method != "PUBLISH" {
		t.Errorf("Method = %q, want PUBLISH when absent", m.Method)
	}
}

func TestParseICSTZID(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:z1", "SUMMARY:x",
		"DTSTART;TZID=America/Lima:20261005T093000",
		"DTEND;TZID=America/Lima:20261005T100000",
		"END:VEVENT", "END:VCALENDAR",
	))
	if want := time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC); !m.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", m.Start.UTC(), want)
	}
	if m.TZ != "America/Lima" {
		t.Errorf("TZ = %q", m.TZ)
	}
}

func TestParseICSWindowsTZID(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:w1",
		"DTSTART;TZID=SA Pacific Standard Time:20261005T093000",
		"DTEND;TZID=SA Pacific Standard Time:20261005T100000",
		"END:VEVENT", "END:VCALENDAR",
	))
	if want := time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC); !m.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", m.Start.UTC(), want)
	}
}

func TestParseICSCustomVTimezoneFallback(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR",
		"BEGIN:VTIMEZONE", "TZID:Mi Zona", "BEGIN:STANDARD", "TZOFFSETTO:-0500", "END:STANDARD", "END:VTIMEZONE",
		"BEGIN:VEVENT", "UID:v1", "DTSTART;TZID=Mi Zona:20261005T090000", "END:VEVENT", "END:VCALENDAR",
	))
	if want := time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC); !m.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", m.Start.UTC(), want)
	}
}

func TestParseICSAllDay(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:d1", "SUMMARY:Retiro",
		"DTSTART;VALUE=DATE:20261005", "END:VEVENT", "END:VCALENDAR",
	))
	if !m.AllDay || m.End.Sub(m.Start) != 24*time.Hour {
		t.Errorf("all-day = %v, span %v", m.AllDay, m.End.Sub(m.Start))
	}
}

func TestParseICSDuration(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:u1",
		"DTSTART:20261005T100000Z", "DURATION:PT1H30M", "END:VEVENT", "END:VCALENDAR",
	))
	if m.End.Sub(m.Start) != 90*time.Minute {
		t.Errorf("span = %v", m.End.Sub(m.Start))
	}
}

func TestParseICSCancel(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "METHOD:CANCEL", "BEGIN:VEVENT", "UID:c1", "SEQUENCE:3",
		"DTSTART:20261005T100000Z", "END:VEVENT", "END:VCALENDAR",
	))
	if !m.Cancelled || m.Method != "CANCEL" || m.Sequence != 3 {
		t.Errorf("cancel = %+v", m)
	}
	s := one(t, ics(
		"BEGIN:VCALENDAR", "METHOD:REQUEST", "BEGIN:VEVENT", "UID:c2", "STATUS:CANCELLED",
		"DTSTART:20261005T100000Z", "END:VEVENT", "END:VCALENDAR",
	))
	if !s.Cancelled {
		t.Error("STATUS:CANCELLED not marked cancelled")
	}
}

func TestParseICSIgnoresReplies(t *testing.T) {
	ms, err := ParseICS(ics(
		"BEGIN:VCALENDAR", "METHOD:REPLY", "BEGIN:VEVENT", "UID:r1",
		"DTSTART:20261005T100000Z", "END:VEVENT", "END:VCALENDAR",
	))
	if err != nil || len(ms) != 0 {
		t.Errorf("REPLY = %v, %v; want nothing", ms, err)
	}
}

func TestParseICSMultipleEvents(t *testing.T) {
	ms, err := ParseICS(ics(
		"BEGIN:VCALENDAR", "METHOD:PUBLISH",
		"BEGIN:VEVENT", "UID:a", "SUMMARY:Uno", "DTSTART:20261005T100000Z", "END:VEVENT",
		"BEGIN:VEVENT", "UID:b", "SUMMARY:Dos", "DTSTART:20261006T100000Z", "END:VEVENT",
		"END:VCALENDAR",
	))
	if err != nil || len(ms) != 2 || ms[0].UID != "a" || ms[1].UID != "b" {
		t.Fatalf("ParseICS = %+v, %v", ms, err)
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if got, _ := Pick(ms, now); got.UID != "b" {
		t.Errorf("Pick = %q, want the one not yet ended", got.UID)
	}
}

func TestParseICSBadTimeIsAnError(t *testing.T) {
	_, err := ParseICS(ics("BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:x", "DTSTART:nonsense", "END:VEVENT", "END:VCALENDAR"))
	if err == nil {
		t.Fatal("want an error for an unreadable DTSTART")
	}
}

func TestParseICSNoCalendarIsAnError(t *testing.T) {
	if _, err := ParseICS("hola"); err == nil {
		t.Fatal("want an error for non-calendar data")
	}
}

func TestParseICSRecurrenceAndRecurrenceID(t *testing.T) {
	m := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:rr", "DTSTART:20261005T100000Z", "DTEND:20261005T110000Z",
		"RRULE:FREQ=WEEKLY;BYDAY=MO", "EXDATE:20261012T100000Z", "END:VEVENT", "END:VCALENDAR",
	))
	if m.Rule != "FREQ=WEEKLY;BYDAY=MO" || len(m.ExDates) != 1 {
		t.Errorf("recurrence = %q %v", m.Rule, m.ExDates)
	}
	o := one(t, ics(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:rr", "RECURRENCE-ID:20261019T100000Z",
		"DTSTART:20261019T120000Z", "END:VEVENT", "END:VCALENDAR",
	))
	if o.UID == "rr" || !strings.HasPrefix(o.UID, "rr#") {
		t.Errorf("override UID = %q, want its own", o.UID)
	}
}

func TestJoinURLExtraction(t *testing.T) {
	cases := []struct {
		name, props, want string
	}{
		{"google property", "X-GOOGLE-CONFERENCE:https://meet.google.com/aaa-bbbb-ccc", "https://meet.google.com/aaa-bbbb-ccc"},
		{"teams in location", "LOCATION:Microsoft Teams Meeting https://teams.microsoft.com/l/meetup-join/19%3ameeting_x/0?context=1", "https://teams.microsoft.com/l/meetup-join/19%3ameeting_x/0?context=1"},
		{"zoom in description", "DESCRIPTION:Join Zoom Meeting\\nhttps://us02web.zoom.us/j/123456789?pwd=abc\\nMeeting ID", "https://us02web.zoom.us/j/123456789?pwd=abc"},
		{"webex in url", "URL:https://acme.webex.com/meet/jdoe", "https://acme.webex.com/meet/jdoe"},
		{"jitsi in x prop", "X-ALT-DESC:<a href=\"https://meet.jit.si/Weekly\">go</a>", "https://meet.jit.si/Weekly"},
		{"safelinks", "DESCRIPTION:https://nam06.safelinks.protection.outlook.com/?url=https%3A%2F%2Fteams.microsoft.com%2Fl%2Fmeetup-join%2Fabc%2F0&data=x", "https://teams.microsoft.com/l/meetup-join/abc/0"},
		{"teams options is not a join link", "DESCRIPTION:https://teams.microsoft.com/meetingOptions/?organizerId=1", ""},
		{"self hosted location", "LOCATION:https://calls.example.org/room1", "https://calls.example.org/room1"},
		{"room only", "LOCATION:Sala 3", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := one(t, ics("BEGIN:VCALENDAR", "BEGIN:VEVENT", "UID:j", "DTSTART:20261005T100000Z", c.props, "END:VEVENT", "END:VCALENDAR"))
			if m.URL != c.want {
				t.Errorf("URL = %q, want %q", m.URL, c.want)
			}
		})
	}
}

func TestFromTextLinks(t *testing.T) {
	m, ok := FromText("Equipo", "mira: https://zoom.us/j/99887766.")
	if !ok || !m.Link || m.URL != "https://zoom.us/j/99887766" || m.UID != "link:https://zoom.us/j/99887766" || m.Summary != "Equipo" {
		t.Errorf("FromText = %+v %v", m, ok)
	}
	if _, ok := FromText("x", "https://example.com/zoom.us/j/1 y https://meet.google.com/landing"); ok {
		t.Error("non-meeting links must not match")
	}
}

func TestFromItemOnlyRecent(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	item := core.Item{ID: "whatsapp:p:1", ThreadName: "Ana", Body: "https://meet.google.com/abc-defg-hij", Timestamp: now.Add(-time.Hour)}
	if m, ok := FromItem(item, now); !ok || m.Summary != "Ana" {
		t.Errorf("recent = %+v %v", m, ok)
	}
	item.Timestamp = now.Add(-25 * time.Hour)
	if _, ok := FromItem(item, now); ok {
		t.Error("an old link must not be detected")
	}
}
