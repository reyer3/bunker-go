package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
)

var meetNow = time.Date(2026, 10, 5, 9, 55, 0, 0, time.UTC) // a Monday

func upcoming(uid, title string, start time.Duration, length time.Duration, url string) core.UpcomingMeeting {
	s := meetNow.Add(start)
	return core.UpcomingMeeting{
		Meeting: core.Meeting{UID: uid, Summary: title, Start: s, End: s.Add(length), URL: url},
		ItemID:  "mail:cl:" + uid,
	}
}

type meetingsClient struct {
	*inboxClient
	meetings []core.UpcomingMeeting
	err      error
	filters  []core.MeetingFilter
}

func (c *meetingsClient) Meetings(_ context.Context, f core.MeetingFilter) ([]core.UpcomingMeeting, error) {
	c.filters = append(c.filters, f)
	return c.meetings, c.err
}

// meetingModel is a loaded model whose poll saw the given meetings, with
// an injected clock and a recording opener.
func meetingModel(t *testing.T, w, h int, list ...core.UpcomingMeeting) (Model, *meetingsClient, *[]string) {
	t.Helper()
	mail := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t", Subject: "Factura",
		From: core.Address{ID: "x@example.com", Name: "Proveedor"}, Unread: true, Timestamp: meetNow}
	client := &meetingsClient{
		inboxClient: &inboxClient{items: []core.Item{mail}, counts: map[core.Channel]map[string]int{core.ChannelMail: {"cl": 1}}},
		meetings:    list,
	}
	m := NewModel(client)
	m.width, m.height = w, h
	m.now = func() time.Time { return meetNow }
	var opened []string
	m.openURL = func(u string) error { opened = append(opened, u); return nil }
	next, _ := m.Update(loadInbox(client, m.pollToken)())
	return next.(Model), client, &opened
}

// run feeds a key's command back through Update, as Bubble Tea would.
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	next, _ := m.Update(cmd())
	return next.(Model)
}

func TestMeetingDaySpanishLabels(t *testing.T) {
	cases := []struct {
		name  string
		start time.Duration
		want  string
	}{
		{"earlier today", -10 * time.Minute, "hoy"},
		{"later today", 7 * time.Hour, "hoy"},
		{"tomorrow", 24 * time.Hour, "mañana"},
		{"weekday", 3 * 24 * time.Hour, "jue 8"},
		{"far", 10 * 24 * time.Hour, "15 oct"},
		{"started yesterday", -24 * time.Hour, "dom 4"},
	}
	for _, c := range cases {
		if got := meetingDay(meetNow.Add(c.start), meetNow); got != c.want {
			t.Errorf("%s: meetingDay = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMeetingOrganizerName(t *testing.T) {
	for in, want := range map[string]string{
		"Ana <ana@example.com>": "Ana",
		"ana@example.com":       "ana@example.com",
		"":                      "",
		"  Equipo  ":            "Equipo",
	} {
		if got := meetingOrganizer(in); got != want {
			t.Errorf("meetingOrganizer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMeetingRowText(t *testing.T) {
	// Day and time of the event, its title, and who invites.
	weekly := upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "u")
	weekly.Organizer = "Ana <ana@example.com>"
	if got := meetingRowText(weekly, meetNow, 60); got != "📅 hoy 10:30 Revisión semanal · Ana" {
		t.Errorf("row = %q", got)
	}
	// Without an organizer the row ends at the title.
	demo := upcoming("b", "Demo", 3*24*time.Hour, time.Hour, "u")
	if got := meetingRowText(demo, meetNow, 60); got != "📅 jue 8 09:55 Demo" {
		t.Errorf("row without organizer = %q", got)
	}
	// A long title is cut; the day, time and organizer stay.
	long := upcoming("a", "Una reunión con un título larguísimo que no cabe", 35*time.Minute, time.Hour, "u")
	long.Organizer = "Ana <ana@example.com>"
	got := meetingRowText(long, meetNow, 34)
	if !strings.HasPrefix(got, "📅 hoy 10:30 ") || !strings.HasSuffix(got, "· Ana") || runewidth.StringWidth(got) > 33 || !strings.Contains(got, "…") {
		t.Errorf("long row = %q (%d cells)", got, runewidth.StringWidth(got))
	}
	// An all-day event has a day but no time.
	allDay := upcoming("d", "Retiro", 0, 24*time.Hour, "")
	allDay.AllDay = true
	if got := meetingRowText(allDay, meetNow, 60); got != "📅 hoy Retiro" {
		t.Errorf("all-day row = %q", got)
	}
	// A bare link only knows who sent it.
	link := core.UpcomingMeeting{Meeting: core.Meeting{Link: true, Summary: "Equipo"}}
	if got := meetingRowText(link, meetNow, 60); got != "🔗 Equipo · enlace" {
		t.Errorf("link row = %q", got)
	}
}

func TestInboxShowsReunionesSectionUnderTheList(t *testing.T) {
	m, client, _ := meetingModel(t, 70, 24,
		upcoming("b", "Demo de producto", 24*time.Hour, 45*time.Minute, "https://zoom.us/j/1"),
		upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"),
	)
	if len(client.filters) != 1 || client.filters[0].Days != meetingsAhead {
		t.Fatalf("Meetings filters = %+v", client.filters)
	}
	view := m.View()
	for _, want := range []string{"Reuniones", "📅 hoy 10:30 Revisión semanal", "📅 mañana 09:55 Demo de producto", "J unirse"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Errorf("view is %d lines, want at most %d", len(lines), m.height)
	}
	if strings.Index(view, "Matrix") > strings.Index(view, "Reuniones") {
		t.Errorf("Reuniones must come under the sections:\n%s", view)
	}
}

func TestSectionHiddenWhenEmptyOrUnsupported(t *testing.T) {
	m, _, _ := meetingModel(t, 70, 24)
	if view := m.View(); strings.Contains(view, "Reuniones") || strings.Contains(view, "J unirse") {
		t.Errorf("empty section is shown:\n%s", view)
	}
	// A client that cannot list meetings leaves the section hidden.
	plain := NewModel(&inboxClient{counts: map[core.Channel]map[string]int{}})
	plain.width, plain.height = 70, 24
	next, _ := plain.Update(loadInbox(plain.client, plain.pollToken)())
	if view := next.(Model).View(); strings.Contains(view, "Reuniones") {
		t.Errorf("section shown without MeetingsClient:\n%s", view)
	}
}

func TestMeetingsKeptWhenARefreshFails(t *testing.T) {
	m, client, _ := meetingModel(t, 70, 24, upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "u"))
	client.err = errors.New("daemon viejo")
	next, _ := m.Update(loadInbox(client, m.pollToken+1)())
	m2 := next.(Model)
	m2.pollToken++ // the failed poll is a fresh one
	m2.polling = true
	next, _ = m2.Update(loadInbox(client, m2.pollToken)())
	if view := next.(Model).View(); !strings.Contains(view, "Revisión semanal") {
		t.Errorf("a failed meetings fetch erased the section:\n%s", view)
	}
}

func TestEndedMeetingLeavesTheSection(t *testing.T) {
	m, _, _ := meetingModel(t, 70, 24, upcoming("a", "Terminando", -50*time.Minute, time.Hour, "u"))
	if !strings.Contains(m.View(), "Terminando") {
		t.Fatal("in-progress meeting not shown")
	}
	m.now = func() time.Time { return meetNow.Add(30 * time.Minute) }
	if strings.Contains(m.View(), "Terminando") {
		t.Error("an ended meeting stays listed")
	}
}

func TestMeetingSectionHeightBudget(t *testing.T) {
	var list []core.UpcomingMeeting
	for i := 0; i < 8; i++ {
		list = append(list, upcoming(string(rune('a'+i)), "Reunión "+string(rune('A'+i)), time.Duration(i+1)*20*time.Minute, time.Hour, "https://zoom.us/j/1"))
	}
	for _, sidebar := range []bool{false, true} {
		for _, h := range []int{0, 6, 9, 11, 12, 14, 16, 20, 30} {
			m, _, _ := meetingModel(t, 50, h, list...)
			m.sidebar = sidebar
			lines := strings.Split(m.View(), "\n")
			// A pane too short for the list itself overflows without
			// meetings too; the section must never add to that.
			bare := m
			bare.meetings = nil
			limit := max(h, len(strings.Split(bare.View(), "\n")))
			if h > 0 && len(lines) > limit {
				t.Errorf("sidebar=%v height %d: view is %d lines, limit %d:\n%s", sidebar, h, len(lines), limit, strings.Join(lines, "\n"))
			}
			if hits := m.inboxHits(); len(hits) != len(lines) {
				t.Errorf("sidebar=%v height %d: %d hits for %d lines", sidebar, h, len(hits), len(lines))
			}
			rows := 0
			for _, l := range lines {
				if strings.Contains(l, "📅") {
					rows++
				}
			}
			if rows > meetingMaxRows {
				t.Errorf("sidebar=%v height %d: %d meeting rows, want at most %d", sidebar, h, rows, meetingMaxRows)
			}
			// The three channel sections (or the sidebar's tabs) keep their place.
			view := strings.Join(lines, "\n")
			if h >= 11 && !sidebar && !(strings.Contains(view, "Mail") && strings.Contains(view, "WhatsApp") && strings.Contains(view, "Matrix")) {
				t.Errorf("height %d: the meetings section pushed a channel section off:\n%s", h, view)
			}
		}
	}
	// With room to spare, the extra rows collapse into a "+N más" notice.
	m, _, _ := meetingModel(t, 50, 40, list...)
	if view := m.View(); !strings.Contains(view, "+5 más") {
		t.Errorf("want a +5 más notice for 8 meetings with 3 rows:\n%s", view)
	}
	// Too short for the list: no section at all.
	m, _, _ = meetingModel(t, 50, 11, list...)
	if strings.Contains(m.View(), "Reuniones") {
		t.Errorf("a pane with no spare room still shows the section:\n%s", m.View())
	}
}

func TestSidebarShowsSectionAndKeepsRows(t *testing.T) {
	m, _, _ := meetingModel(t, 36, 18, upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "u"))
	m.sidebar = true
	view := m.View()
	if !strings.Contains(view, "📅 hoy 10:30 Revisión") || !strings.Contains(view, "Factura") {
		t.Errorf("sidebar view:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if runewidth.StringWidth(stripANSI(l)) > m.width {
			t.Errorf("line %q is wider than the pane", l)
		}
	}
}

func TestJKeyJoinsNextMeetingWithALink(t *testing.T) {
	m, _, opened := meetingModel(t, 70, 24,
		upcoming("a", "Sin enlace", 10*time.Minute, time.Hour, ""),
		upcoming("b", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"),
	)
	next, cmd := m.Update(runeKey("J"))
	m = next.(Model)
	if flash, _ := m.currentFlash(); flash != "Abriendo reunión…" {
		t.Errorf("flash = %q, want Abriendo reunión…", flash)
	}
	runCmd(t, m, cmd)
	if len(*opened) != 1 || (*opened)[0] != "https://meet.google.com/abc-defg-hij" {
		t.Fatalf("opened = %v, want the first meeting that has a link", *opened)
	}
}

func TestJKeyErrorsAreLoud(t *testing.T) {
	m, _, opened := meetingModel(t, 70, 24)
	next, cmd := m.Update(runeKey("J"))
	if cmd != nil || len(*opened) != 0 {
		t.Fatal("J with no meetings must not open anything")
	}
	if flash, _ := next.(Model).currentFlash(); flash != "No hay reuniones próximas" {
		t.Errorf("flash = %q", flash)
	}

	m, _, opened = meetingModel(t, 70, 24, upcoming("a", "Sin enlace", 10*time.Minute, time.Hour, ""))
	next, cmd = m.Update(runeKey("J"))
	if cmd != nil || len(*opened) != 0 {
		t.Fatal("J with no link must not open anything")
	}
	if flash, _ := next.(Model).currentFlash(); !strings.Contains(flash, "Ninguna reunión próxima trae enlace") {
		t.Errorf("flash = %q", flash)
	}
}

func TestOpenerFailureShowsAnError(t *testing.T) {
	m, _, _ := meetingModel(t, 70, 24, upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"))
	m.openURL = func(string) error { return errors.New("openurl: no se encontró \"xdg-open\"") }
	next, cmd := m.Update(runeKey("J"))
	m = runCmd(t, next.(Model), cmd)
	flash, _ := m.currentFlash()
	if !strings.HasPrefix(flash, "No se pudo abrir la reunión:") || !strings.Contains(flash, "xdg-open") {
		t.Errorf("flash = %q", flash)
	}
}

func meetingRowLine(t *testing.T, m Model, title string) int {
	t.Helper()
	for i, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, title) && strings.Contains(l, "📅") {
			return i
		}
	}
	t.Fatalf("no meeting row for %q:\n%s", title, m.View())
	return -1
}

func click(m Model, y int) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return next.(Model), cmd
}

func TestClickOnMeetingRowOpensItsLink(t *testing.T) {
	m, _, opened := meetingModel(t, 70, 24,
		upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"),
		upcoming("b", "Demo de producto", 3*time.Hour, time.Hour, "https://us02web.zoom.us/j/99"),
	)
	next, cmd := click(m, meetingRowLine(t, m, "Demo de producto"))
	if flash, _ := next.currentFlash(); flash != "Abriendo reunión…" {
		t.Errorf("flash = %q", flash)
	}
	runCmd(t, next, cmd)
	if len(*opened) != 1 || (*opened)[0] != "https://us02web.zoom.us/j/99" {
		t.Fatalf("opened = %v, want the clicked meeting's link", *opened)
	}
}

func TestClickOnMeetingWithoutLinkSaysSo(t *testing.T) {
	m, _, opened := meetingModel(t, 70, 24, upcoming("a", "Sala 3", 35*time.Minute, time.Hour, ""))
	next, cmd := click(m, meetingRowLine(t, m, "Sala 3"))
	if cmd != nil || len(*opened) != 0 {
		t.Fatal("a meeting without a link must not open anything")
	}
	if flash, _ := next.currentFlash(); flash != "La reunión «Sala 3» no trae enlace para unirse" {
		t.Errorf("flash = %q", flash)
	}
}

func TestClickWorksInTheSidebar(t *testing.T) {
	m, _, opened := meetingModel(t, 40, 20, upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"))
	m.sidebar = true
	next, cmd := click(m, meetingRowLine(t, m, "Revisión semanal"))
	runCmd(t, next, cmd)
	if len(*opened) != 1 {
		t.Fatalf("opened = %v", *opened)
	}
}

func TestPaletteJoinsTheNextMeeting(t *testing.T) {
	m, _, opened := meetingModel(t, 70, 24, upcoming("a", "Revisión semanal", 35*time.Minute, time.Hour, "https://meet.google.com/abc-defg-hij"))
	var entry *paletteEntry
	for _, e := range m.openPalette().paletteCommands() {
		if e.label == "Unirse a la próxima reunión" {
			e := e
			entry = &e
		}
	}
	if entry == nil || entry.reason != "" || entry.key != "J" {
		t.Fatalf("palette entry = %+v", entry)
	}
	next, cmd := m.Update(entry.msg)
	runCmd(t, next.(Model), cmd)
	if len(*opened) != 1 {
		t.Fatalf("opened = %v", *opened)
	}
	empty, _, _ := meetingModel(t, 70, 24)
	for _, e := range empty.paletteCommands() {
		if e.label == "Unirse a la próxima reunión" && e.reason == "" {
			t.Error("palette offers joining with no meetings")
		}
	}
}

func TestDefaultOpenerHonoursEnvOverride(t *testing.T) {
	m := NewModel(nil)
	m.getenv = func(k string) string {
		if k == "BUNKER_OPEN_URL" {
			return "/no/such/opener"
		}
		return ""
	}
	err := m.openMeetingURL("https://meet.google.com/abc-defg-hij")
	if err == nil || !strings.Contains(err.Error(), "BUNKER_OPEN_URL") {
		t.Fatalf("err = %v, want it to name BUNKER_OPEN_URL", err)
	}
	if err := m.openMeetingURL("javascript:alert(1)"); err == nil {
		t.Fatal("a non-http link must be refused")
	}
}
