package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/openurl"
)

// The "Reuniones" section sits under the conversation list (inbox and
// sidebar): the next meetings from calendar invitations and recent call
// links, each with its time. A click on a row, or J for the next one with
// a link, opens the meeting's URL with the desktop opener on this machine
// (openurl: xdg-open, or $BUNKER_OPEN_URL). The section is hidden while
// there is nothing to show, and it only takes the room the list can
// spare, so it never pushes a conversation off screen.

// MeetingsClient is the optional Client capability behind the section:
// the RPC client and the TUI's query client implement it. Against a
// daemon without it the section stays hidden.
type MeetingsClient interface {
	Meetings(ctx context.Context, filter core.MeetingFilter) ([]core.UpcomingMeeting, error)
}

const (
	// meetingMaxRows bounds the section's rows however tall the pane is.
	meetingMaxRows = 4
	// meetingsAhead is how many days the section looks ahead.
	meetingsAhead = 2
	// meetingJoinKey joins the next meeting that has a link.
	meetingJoinKey = "J"
)

// fetchMeetings loads the upcoming meetings; ok is false when the client
// cannot list them or the call failed, so the poll keeps what it showed.
func fetchMeetings(ctx context.Context, client Client) (meetings []core.UpcomingMeeting, ok bool) {
	lister, can := client.(MeetingsClient)
	if !can {
		return nil, false
	}
	list, err := lister.Meetings(ctx, core.MeetingFilter{Days: meetingsAhead, Limit: 20})
	if err != nil {
		return nil, false
	}
	return list, true
}

// meetingOpenedMsg is the opener's answer to a join.
type meetingOpenedMsg struct {
	title string
	err   error
}

// activeMeetings are the loaded meetings still worth listing at now: a
// timed one that has ended since the last poll is dropped at once.
func (m Model) activeMeetings() []core.UpcomingMeeting {
	now := m.clock()
	out := make([]core.UpcomingMeeting, 0, len(m.meetings))
	for _, u := range m.meetings {
		if !u.Link && !u.End.After(now) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// openMeetingURL runs the opener: the injected one in tests, else the
// desktop's.
func (m Model) openMeetingURL(rawURL string) error {
	if m.openURL != nil {
		return m.openURL(rawURL)
	}
	getenv := m.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	return openurl.Open(rawURL, getenv)
}

// joinMeeting opens the join link of active meeting i. A meeting without
// one, or an opener that cannot run, is a flash saying so, never a
// silent no-op.
func (m Model) joinMeeting(i int) (Model, tea.Cmd) {
	list := m.activeMeetings()
	if i < 0 || i >= len(list) {
		return m, nil
	}
	u := list[i]
	title := meetingTitle(u)
	if strings.TrimSpace(u.URL) == "" {
		return m.withFlash("La reunión «" + title + "» no trae enlace para unirse"), nil
	}
	m = m.withFlash("Abriendo reunión…")
	open := m.openMeetingURL
	return m, func() tea.Msg { return meetingOpenedMsg{title: title, err: open(u.URL)} }
}

// joinNextMeeting is the J key: the first listed meeting that has a link.
func (m Model) joinNextMeeting() (Model, tea.Cmd) {
	list := m.activeMeetings()
	if len(list) == 0 {
		return m.withFlash("No hay reuniones próximas"), nil
	}
	for i, u := range list {
		if strings.TrimSpace(u.URL) != "" {
			return m.joinMeeting(i)
		}
	}
	return m.withFlash("Ninguna reunión próxima trae enlace para unirse"), nil
}

func (m Model) handleMeetingOpened(msg meetingOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m.withFlash("No se pudo abrir la reunión: " + humanError(msg.err)), nil
	}
	return m, nil
}

// joinReason says why J cannot run now, or "" (the palette dims it).
func (m Model) joinReason() string {
	list := m.activeMeetings()
	if len(list) == 0 {
		return "no hay reuniones próximas"
	}
	for _, u := range list {
		if strings.TrimSpace(u.URL) != "" {
			return ""
		}
	}
	return "ninguna reunión trae enlace"
}

// withMeetingHint adds "J unirse" to a hint line while there is a meeting
// to join, right after "↵ abrir".
func (m Model) withMeetingHint(hints []keyHint) []keyHint {
	if len(m.activeMeetings()) == 0 {
		return hints
	}
	at := 0
	for i, h := range hints {
		if h.key == "↵" {
			at = i + 1
			break
		}
	}
	out := make([]keyHint, 0, len(hints)+1)
	out = append(out, hints[:at]...)
	out = append(out, keyHint{meetingJoinKey, "unirse", false})
	return append(out, hints[at:]...)
}

func meetingTitle(u core.UpcomingMeeting) string {
	title := strings.Join(strings.Fields(safeLine(u.Summary)), " ")
	if title == "" {
		title = "Reunión"
	}
	return title
}

var spanishWeekdays = [...]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}

// meetingWhen is the relative label after a row's title: "ahora", "en 25
// min", "en 1 h 30 min", then the day for later ones ("hoy", "mañana",
// "jue 8", "12 oct"). A link meeting has no time: "enlace".
func meetingWhen(u core.UpcomingMeeting, now time.Time) string {
	if u.Link {
		return "enlace"
	}
	if u.InProgress(now) {
		return "ahora"
	}
	start := u.Start.In(now.Location())
	if d := start.Sub(now); d > 0 && d < 6*time.Hour && sameDay(start, now) && !u.AllDay {
		switch {
		case d < time.Minute:
			return "en 1 min"
		case d < time.Hour:
			return fmt.Sprintf("en %d min", int(d.Minutes()))
		}
		h, min := int(d.Hours()), int(d.Minutes())%60
		if min == 0 {
			return fmt.Sprintf("en %d h", h)
		}
		return fmt.Sprintf("en %d h %d min", h, min)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, now.Location())
	switch days := int(day.Sub(today).Hours() / 24); {
	case days <= 0:
		return "hoy"
	case days == 1:
		return "mañana"
	case days < 7:
		return fmt.Sprintf("%s %d", spanishWeekdays[start.Weekday()], start.Day())
	}
	return fmt.Sprintf("%d %s", start.Day(), spanishMonths[start.Month()-1])
}

// meetingRowText is one row without styling:
// "📅 10:30 Revisión semanal · en 25 min". A link meeting has no clock and
// an all-day event shows none either; the label after the title stays
// visible when the title has to be cut.
func meetingRowText(u core.UpcomingMeeting, now time.Time, width int) string {
	icon := "📅 "
	if u.Link {
		icon = "🔗 "
	}
	clock := ""
	if !u.Link && !u.AllDay {
		clock = u.Start.In(now.Location()).Format("15:04") + " "
	}
	suffix := " · " + meetingWhen(u, now)
	title := meetingTitle(u)
	if width > 0 {
		budget := width - runewidth.StringWidth(icon+clock+suffix) - 1
		if budget < 1 {
			budget = 1
		}
		title = runewidth.Truncate(title, budget, "…")
	}
	return truncatePlain(icon+clock+title+suffix, max(width-1, 0))
}

// meetingSection renders the header and up to avail-1 rows (avail <= 0
// means none; a row beyond the cap becomes a "+N más" notice). Every
// line is a hit on its meeting, so a click anywhere on the row joins.
func (m Model) meetingSection(styles rowStyles, width, avail int) (lines []string, hits []inboxHit) {
	list := m.activeMeetings()
	if !m.loaded || len(list) == 0 || avail < 2 {
		return nil, nil
	}
	now := m.clock()
	rows := min(len(list), meetingMaxRows, avail-1)
	more := 0
	if rows < len(list) && rows >= 2 {
		rows--
		more = len(list) - rows
	}
	label := "─ Reuniones "
	rule := ""
	if w := widthOrDefault(width) - runewidth.StringWidth(label); w > 0 {
		rule = strings.Repeat("─", w)
	}
	lines = append(lines, styles.dim.Render(truncatePlain(label+rule, widthOrDefault(width))))
	hits = append(hits, inboxHit{kind: hitNone})
	for i := 0; i < rows; i++ {
		text := " " + meetingRowText(list[i], now, width)
		switch {
		case list[i].InProgress(now):
			text = styles.title.Render(text)
		case list[i].Link:
			text = styles.dim.Render(text)
		}
		lines = append(lines, text)
		hits = append(hits, inboxHit{kind: hitMeeting, row: i})
	}
	if more > 0 {
		lines = append(lines, styles.dim.Render(truncatePlain(fmt.Sprintf(" +%d más", more), width)))
		hits = append(hits, inboxHit{kind: hitNone})
	}
	return lines, hits
}
