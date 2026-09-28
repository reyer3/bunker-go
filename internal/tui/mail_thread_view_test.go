package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestThreadSubjectShownAsBoldTitle pins K8's bold Subject title: it must
// be the first line and carry actual styling under a forced color
// profile (not just plain text that happens to contain the subject).
func TestThreadSubjectShownAsBoldTitle(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	client := &replyClient{}
	model := mailThreadReadyModel(client, "mail:cl:1")
	model.render = r
	model, cmd := openThread(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	firstLine := strings.SplitN(model.View(), "\n", 2)[0]
	if !strings.Contains(stripANSI(firstLine), "hola") {
		t.Fatalf("title line = %q, want the Subject %q", firstLine, "hola")
	}
	if stripANSI(firstLine) == firstLine {
		t.Fatal("subject title carries no styling at all under a forced TrueColor profile")
	}
}

// TestThreadExpandedMessageFetchesBodyOnOpenAndCachesIt is K8's core
// behavior: mail sync stores headers only (Body arrives empty until
// fetched), so the auto-expanded newest message must fetch its full body
// via Read(id, receipt=false) on open, and a later re-expand of the same
// message must never re-fetch it (cached per id).
func TestThreadExpandedMessageFetchesBodyOnOpenAndCachesIt(t *testing.T) {
	client := &replyClient{}
	client.readResults = map[string]core.Item{
		"mail:cl:2": {Body: "fetched body two"},
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	model := mailThreadReadyModel(client, "mail:cl:2")
	model.now = func() time.Time { return now }
	model, cmd := openThread(model)
	msg := cmd().(threadLoadedMsg)
	msg.items = []core.Item{
		{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Bob"}, Subject: "hola", Timestamp: now.Add(-time.Hour)},
		{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Alice"}, Subject: "Re: hola", Timestamp: now},
	}
	updated, fetchCmd := model.Update(msg)
	model = updated.(Model)
	if fetchCmd == nil {
		t.Fatal("opening the thread did not request the auto-expanded (newest) message's body")
	}
	updated, _ = model.Update(fetchCmd())
	model = updated.(Model)
	if !strings.Contains(model.View(), "fetched body two") {
		t.Fatalf("thread view = %q, want the fetched body", model.View())
	}
	if len(client.readIDs) != 1 || client.readIDs[0] != "mail:cl:2" {
		t.Fatalf("Read calls = %+v, want exactly one for mail:cl:2", client.readIDs)
	}

	// Collapse then re-expand the same (already-fetched) message: no
	// second fetch.
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, again := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if again != nil {
		t.Fatal("re-expanding an already-fetched message issued another fetch command")
	}
	if len(client.readIDs) != 1 {
		t.Fatalf("Read calls after re-expand = %d, want still 1 (cached)", len(client.readIDs))
	}
}

// TestThreadBodyFetchDiscardsStaleTokenAfterReopeningAnotherThread is the
// mutation-checked token-staleness case: a body-fetch result for a thread
// that is no longer the one being viewed (the user closed it and opened a
// different conversation before the fetch landed) must be silently
// discarded, never cached into the new, unrelated thread.
func TestThreadBodyFetchDiscardsStaleTokenAfterReopeningAnotherThread(t *testing.T) {
	client := &replyClient{}
	itemA := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", Subject: "a"}
	itemB := core.Item{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Thread: "t2", Subject: "b"}
	model := NewModel(client).withGlyphs(nil)
	model.width, model.height, model.loaded = 60, 20, true

	client.threadItems = []core.Item{itemA}
	model, loadCmdA := model.openThread(itemA)
	updated, _ := model.Update(loadCmdA())
	model = updated.(Model)
	staleToken := model.threadToken

	client.threadItems = []core.Item{itemB}
	model, loadCmdB := model.openThread(itemB)
	if model.threadToken == staleToken {
		t.Fatal("reopening a different thread must bump threadToken")
	}
	updated, _ = model.Update(loadCmdB())
	model = updated.(Model)

	stale := threadBodyLoadedMsg{token: staleToken, id: itemA.ID, body: "should never be cached"}
	updated, cmd := model.Update(stale)
	model = updated.(Model)
	if cmd != nil {
		t.Fatal("a stale body-fetch result must not trigger any further command")
	}
	if _, ok := model.threadBodies[itemA.ID]; ok {
		t.Fatal("a stale (superseded) body-fetch result must not be cached into the current thread")
	}
}

// TestThreadCollapsedSnippetNeverRepeatsSenderName pins the reported K8
// bug fix: the collapsed row's snippet used to be previewLine's "Sender:
// body" format, which repeated the sender already shown before the "·".
// The snippet must come from the body (once fetched) or the Subject,
// never the sender's name again.
func TestThreadCollapsedSnippetNeverRepeatsSenderName(t *testing.T) {
	client := &replyClient{}
	client.readResults = map[string]core.Item{
		"mail:cl:2": {Body: "the real reply body"},
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	model := mailThreadReadyModel(client, "mail:cl:2")
	model.now = func() time.Time { return now }
	model, cmd := openThread(model)
	msg := cmd().(threadLoadedMsg)
	msg.items = []core.Item{
		{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Bob"}, Subject: "asunto original", Timestamp: now.Add(-time.Hour)},
		{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Alice"}, Subject: "Re: asunto original", Timestamp: now},
	}
	updated, _ := model.Update(msg)
	model = updated.(Model)

	view := model.View()
	if strings.Contains(view, "Bob · ") && strings.Contains(view, "Bob:") {
		t.Fatalf("thread view = %q, want the collapsed snippet to never repeat the sender's name", view)
	}
	if !strings.Contains(view, "Bob · ") {
		t.Fatalf("thread view = %q, want the collapsed row's sender prefix", view)
	}
	if !strings.Contains(view, "asunto original") {
		t.Fatalf("thread view = %q, want the collapsed row's snippet to fall back to the Subject (no body fetched for it yet)", view)
	}
}

// TestThreadQuotedLinesAreDimmed pins the ">"-quoted-line dimming: a
// quoted original (as quoteOriginal renders it into a reply/forward
// draft, or as any message body containing "> " lines) must render those
// lines with different styling than the rest of the body under a forced
// color profile.
func TestThreadQuotedLinesAreDimmed(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	client := &replyClient{}
	client.readResults = map[string]core.Item{
		"mail:cl:1": {Body: "my new reply\n> quoted original line"},
	}
	client.threadItems = []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{Name: "Bob"}, Subject: "hola",
	}}
	model := mailThreadReadyModel(client, "mail:cl:1")
	model.render = r
	model, cmd := openThread(model)
	updated, fetchCmd := model.Update(cmd())
	model = updated.(Model)
	if fetchCmd != nil {
		updated, _ = model.Update(fetchCmd())
		model = updated.(Model)
	}

	view := model.View()
	lines := strings.Split(view, "\n")
	var plainLine, quotedLine string
	for _, l := range lines {
		if strings.Contains(l, "my new reply") {
			plainLine = l
		}
		if strings.Contains(l, "quoted original line") {
			quotedLine = l
		}
	}
	if plainLine == "" || quotedLine == "" {
		t.Fatalf("view = %q, want both the plain and quoted body lines", view)
	}
	if stripANSI(quotedLine) == quotedLine {
		t.Fatal("quoted line carries no styling at all under a forced TrueColor profile")
	}
}

// TestThreadViewFitsPaneHeightWithLongExpandedBody mirrors the chat
// view's live-reported height-overflow bug for the mail thread view: a
// long expanded body must scroll WITHIN the view instead of pushing the
// Subject title (or the footer keymap) off screen.
func TestThreadViewFitsPaneHeightWithLongExpandedBody(t *testing.T) {
	var bodyLines []string
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("linea %d del cuerpo", i))
	}
	longBody := strings.Join(bodyLines, "\n")

	item := core.Item{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1",
		From: core.Address{Name: "Bob"}, Subject: "un asunto largo",
	}
	client := &replyClient{}
	client.readResults = map[string]core.Item{"mail:cl:1": {Body: longBody}}
	client.threadItems = []core.Item{item}
	model := NewModel(client).withGlyphs(nil)
	model.width, model.height = 62, 20
	model, cmd := model.openThread(item)
	updated, fetchCmd := model.Update(cmd())
	model = updated.(Model)
	if fetchCmd != nil {
		updated, _ = model.Update(fetchCmd())
		model = updated.(Model)
	}

	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) > model.height {
		t.Fatalf("thread view has %d lines, want <= pane height %d", len(lines), model.height)
	}
	if !strings.Contains(lines[0], "un asunto largo") {
		t.Fatalf("line 0 = %q, want the Subject title to stay the first, always-visible line", lines[0])
	}
	if !strings.Contains(view, "responder") {
		t.Fatalf("view = %q, want the footer keymap hint still visible", view)
	}
}

// TestThreadPgDownScrollsIntoLongExpandedBodyThenPgUpBack pins PgUp/PgDown
// scrolling a long expanded body within the view, and back.
func TestThreadPgDownScrollsIntoLongExpandedBodyThenPgUpBack(t *testing.T) {
	var bodyLines []string
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("linea %d del cuerpo", i))
	}
	longBody := strings.Join(bodyLines, "\n")
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Bob"}, Subject: "asunto"}
	client := &replyClient{}
	client.readResults = map[string]core.Item{"mail:cl:1": {Body: longBody}}
	client.threadItems = []core.Item{item}
	model := NewModel(client).withGlyphs(nil)
	model.width, model.height = 62, 20
	model, cmd := model.openThread(item)
	updated, fetchCmd := model.Update(cmd())
	model = updated.(Model)
	if fetchCmd != nil {
		updated, _ = model.Update(fetchCmd())
		model = updated.(Model)
	}
	if !strings.Contains(model.View(), "linea 0 del cuerpo") {
		t.Fatalf("view = %q, want the body's start visible by default", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if strings.Contains(model.View(), "linea 0 del cuerpo") {
		t.Fatalf("view after PgDown = %q, want the body's start scrolled out of view", model.View())
	}
	if !strings.Contains(model.View(), "asunto") {
		t.Fatalf("view after PgDown = %q, want the Subject title still visible", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if !strings.Contains(model.View(), "linea 0 del cuerpo") {
		t.Fatalf("view after PgDown then PgUp = %q, want the body's start visible again", model.View())
	}
}

// TestThreadSelectionChangeResetsScrollToSelected pins the "must stay
// visible" fix: scrolling into one message's long body, then moving the
// selection with j/k, must bring the newly selected item's own start
// back into view instead of leaving the window wherever it was.
func TestThreadSelectionChangeResetsScrollToSelected(t *testing.T) {
	var bodyLines []string
	for i := 0; i < 40; i++ {
		bodyLines = append(bodyLines, fmt.Sprintf("linea %d", i))
	}
	longBody := strings.Join(bodyLines, "\n")
	older := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Bob"}, Subject: "asunto", Timestamp: time.Unix(0, 0)}
	newest := core.Item{ID: "mail:cl:2", Channel: core.ChannelMail, Account: "cl", Thread: "t1", From: core.Address{Name: "Alice"}, Subject: "Re: asunto", Timestamp: time.Unix(100, 0)}
	client := &replyClient{}
	client.readResults = map[string]core.Item{"mail:cl:2": {Body: longBody}}
	client.threadItems = []core.Item{older, newest}
	model := NewModel(client).withGlyphs(nil)
	model.width, model.height = 62, 20
	model, cmd := model.openThread(newest)
	updated, fetchCmd := model.Update(cmd())
	model = updated.(Model)
	if fetchCmd != nil {
		updated, _ = model.Update(fetchCmd())
		model = updated.(Model)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if strings.Contains(model.View(), "Bob · ") {
		t.Fatalf("view after PgDown = %q, want the (collapsed) older message scrolled out of view", model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	model = updated.(Model)
	if !strings.Contains(model.View(), "Bob · ") {
		t.Fatalf("view after selecting the older message = %q, want it scrolled back into view", model.View())
	}
}

// TestWalkthroughMailThreadGolden goldens the K8 mail thread view's final
// rendered screen at a fixed clock and a forced TrueColor profile.
func TestWalkthroughMailThreadGolden(t *testing.T) {
	at := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	client := &replyClient{}
	client.readResults = map[string]core.Item{
		"mail:work:2": {Body: "Gracias por el reporte, lo reviso hoy."},
	}
	client.items = []core.Item{{
		ID: "mail:work:2", Channel: core.ChannelMail, Account: "work",
		Subject: "Re: Status update", From: core.Address{ID: "alice@example.com", Name: "Alice"},
		Unread: true, Timestamp: at,
	}}
	client.threadItems = []core.Item{
		{ID: "mail:work:1", Channel: core.ChannelMail, Account: "work", Thread: "t1", Subject: "Status update", From: core.Address{ID: "bob@example.com", Name: "Bob"}, Timestamp: at.Add(-time.Hour)},
		{ID: "mail:work:2", Channel: core.ChannelMail, Account: "work", Thread: "t1", Subject: "Re: Status update", From: core.Address{ID: "alice@example.com", Name: "Alice"}, To: []core.Address{{ID: "bob@example.com", Name: "Bob"}}, Timestamp: at},
	}

	model := NewModel(client)
	model.now = func() time.Time { return at }
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	model.render = r

	updated, _ := model.Update(model.Init()())
	m := updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m = updated.(Model)

	// Mail wraps this conversation under a collapsible sender row
	// (mail-sender-groups.md): expand it and move onto the nested thread
	// row before Enter opens it, matching what a user sees.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)

	updated, openCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, fetchCmd := m.Update(openCmd())
	m = updated.(Model)
	if fetchCmd != nil {
		updated, _ = m.Update(fetchCmd())
		m = updated.(Model)
	}

	teatest.RequireEqualOutput(t, []byte(m.View()))
}
