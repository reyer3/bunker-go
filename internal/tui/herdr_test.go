package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

// fakeAsker records the item ids "a" asked about and answers err.
type fakeAsker struct {
	asked []string
	err   error
}

func (f *fakeAsker) ask(_ context.Context, id string) error {
	f.asked = append(f.asked, id)
	return f.err
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// pressAsk presses key and, when it returns a command, runs it and feeds
// its message back, as Bubble Tea would.
func pressAsk(t *testing.T, m Model, key tea.KeyMsg) (Model, bool) {
	t.Helper()
	next, cmd := m.Update(key)
	m = next.(Model)
	if cmd == nil {
		return m, false
	}
	if !m.asking {
		t.Fatal("asking is not set while the ask runs")
	}
	next, _ = m.Update(cmd())
	return next.(Model), true
}

func TestSidebarAskKey(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantFlash string
	}{
		{"sent", nil, "enviado a Claude"},
		{"blocked", fmt.Errorf("herdr: agent prompt w1:p2: %w", ErrAgentBlocked), "Claude está esperando tu respuesta en su panel"},
		{"no claude", errors.New("herdr: no hay ningún Claude Code en herdr"), "no se pudo preguntar a Claude: no hay ningún Claude Code en herdr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asker := &fakeAsker{err: tc.err}
			m, ran := pressAsk(t, sidebarModel(WithAgentAsker(asker.ask)), runeKey("a"))
			if !ran || len(asker.asked) != 1 || asker.asked[0] != "whatsapp:personal:1" {
				t.Fatalf("asked = %v, want the selected conversation's newest item", asker.asked)
			}
			if m.asking {
				t.Fatal("asking still set after the ask finished")
			}
			if flash, _ := m.currentFlash(); flash != tc.wantFlash {
				t.Fatalf("flash = %q, want %q", flash, tc.wantFlash)
			}
			if !strings.Contains(m.View(), tc.wantFlash[:10]) {
				t.Fatalf("the sidebar does not show the flash:\n%s", m.View())
			}
		})
	}
}

func TestSidebarAskKeyWithoutAskerDoesNothing(t *testing.T) {
	m := sidebarModel()
	next, cmd := m.Update(runeKey("a"))
	if cmd != nil || next.(Model).asking {
		t.Fatal("a without an asker started something")
	}
	if _, ok := next.(Model).currentFlash(); ok {
		t.Fatal("a without an asker flashed")
	}
}

func TestAskKeyIgnoredWhileAnAskRuns(t *testing.T) {
	asker := &fakeAsker{}
	m := sidebarModel(WithAgentAsker(asker.ask))
	next, cmd := m.Update(runeKey("a"))
	if cmd == nil {
		t.Fatal("first a returned no command")
	}
	if _, cmd := next.(Model).Update(runeKey("a")); cmd != nil {
		t.Fatal("a second a while the first runs started another ask")
	}
}

func TestSidebarAskHintAndHelpOnlyWithAsker(t *testing.T) {
	without := sidebarModel()
	without.width = 80
	with := sidebarModel(WithAgentAsker((&fakeAsker{}).ask))
	with.width = 80
	if strings.Contains(without.View(), "a Claude") {
		t.Fatal("the hint shows without an asker")
	}
	if !strings.Contains(with.View(), "↵ abrir · a Claude") {
		t.Fatalf("the hint does not follow ↵ with an asker:\n%s", with.View())
	}
	with.height = 0
	if help := with.openHelp().helpView(); !strings.Contains(help, "preguntar a Claude") {
		t.Fatalf("help lacks the ask key:\n%s", help)
	}
	without.height = 0
	if help := without.openHelp().helpView(); strings.Contains(help, "preguntar a Claude") {
		t.Fatal("help shows the ask key without an asker")
	}
}

func startOpenWith(t *testing.T, client *inboxClient, id string, opts ...Option) Model {
	t.Helper()
	m := NewModel(client, append([]Option{WithOpenItem(id)}, opts...)...).withGlyphs(nil)
	m.width, m.height = 80, 20
	next, _ := m.Update(m.Init()())
	return next.(Model)
}

func TestOpenChatAsksWithAltA(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t1",
		From: core.Address{Name: "Alice"},
	}}
	asker := &fakeAsker{}
	m := startOpenWith(t, client, "whatsapp:personal:1", WithAgentAsker(asker.ask))
	if !m.chatMode {
		t.Fatal("not in the chat view")
	}
	if !strings.Contains(m.View(), "Alt+A Claude") {
		t.Fatalf("chat hints lack Alt+A:\n%s", m.View())
	}

	// "a" is text in a chat.
	next, _ := m.Update(runeKey("a"))
	if got := next.(Model).composer.Value(); got != "a" || len(asker.asked) != 0 {
		t.Fatalf("a in a chat: composer = %q, asked = %v; want it typed", got, asker.asked)
	}

	m, ran := pressAsk(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a"), Alt: true})
	if !ran || len(asker.asked) != 1 || asker.asked[0] != "whatsapp:personal:1" {
		t.Fatalf("asked = %v, want the open item", asker.asked)
	}
	if !strings.Contains(m.View(), "enviado a Claude") {
		t.Fatalf("the chat does not show the result:\n%s", m.View())
	}
}

func TestOpenMailThreadAsksWithA(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "th", Subject: "Reunión",
	}}
	asker := &fakeAsker{err: fmt.Errorf("herdr: %w", ErrAgentBlocked)}
	m := startOpenWith(t, client, "mail:cl:1", WithAgentAsker(asker.ask))
	if !m.threadMode {
		t.Fatal("not in the mail thread view")
	}
	if !strings.Contains(m.View(), "a Claude") {
		t.Fatalf("thread hints lack a:\n%s", m.View())
	}
	m, ran := pressAsk(t, m, runeKey("a"))
	if !ran || len(asker.asked) != 1 || asker.asked[0] != "mail:cl:1" {
		t.Fatalf("asked = %v, want the open item", asker.asked)
	}
	if !strings.Contains(m.View(), "Claude está esperando tu respuesta en su panel") {
		t.Fatalf("the thread does not show the blocked notice:\n%s", m.View())
	}
}

func TestOpenMailThreadWithoutAskerHasNoHint(t *testing.T) {
	client := &inboxClient{readResult: core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "th"}}
	m := startOpenWith(t, client, "mail:cl:1")
	if strings.Contains(m.View(), "Claude") {
		t.Fatalf("thread hints mention Claude without an asker:\n%s", m.View())
	}
	if _, cmd := m.Update(runeKey("a")); cmd != nil {
		t.Fatal("a without an asker returned a command")
	}
}

func TestUnreadReportAfterPollAndFailureFlashesOnce(t *testing.T) {
	var got []int
	fail := errors.New("herdr: pane report-metadata: no server")
	var answer error
	m := sidebarModel(WithUnreadReporter(func(n int) error {
		got = append(got, n)
		return answer
	}))
	m.counts = map[core.Channel]map[string]int{
		core.ChannelMail:     {"a": 2, "b": 1},
		core.ChannelWhatsApp: {"personal": 4},
	}
	cmd := m.reportUnread()
	if cmd == nil {
		t.Fatal("no report command")
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("reported %v, want the unread total 7", got)
	}
	if _, ok := m.currentFlash(); ok {
		t.Fatal("a successful report flashed")
	}

	answer = fail
	next, _ = m.Update(m.reportUnread()())
	m = next.(Model)
	flash, ok := m.currentFlash()
	if !ok || !strings.Contains(flash, "no se pudo avisar a herdr") {
		t.Fatalf("flash = %q, want the report failure", flash)
	}
	m = m.withFlash("otra cosa")
	next, _ = m.Update(m.reportUnread()())
	if flash, _ := next.(Model).currentFlash(); flash != "otra cosa" {
		t.Fatalf("a repeated failure flashed again: %q", flash)
	}
	m = next.(Model)

	// A success re-arms it.
	answer = nil
	next, _ = m.Update(m.reportUnread()())
	answer = fail
	next, _ = next.(Model).Update(m.reportUnread()())
	if flash, _ := next.(Model).currentFlash(); !strings.Contains(flash, "no se pudo avisar a herdr") {
		t.Fatalf("a failure after a success did not flash: %q", flash)
	}

	if (sidebarModel()).reportUnread() != nil {
		t.Fatal("reportUnread without a reporter returned a command")
	}
}

func TestMessageNotifierRateLimitAndBody(t *testing.T) {
	var bodies []string
	var osc bytes.Buffer
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m := notifyReadyModel(&osc)
	m.blurred = false // herdr notifications do not depend on focus
	m.messageNotify = func(body string) error {
		bodies = append(bodies, body)
		return nil
	}
	m.now = func() time.Time { return now }
	arrive := func(items ...core.Item) {
		t.Helper()
		old := m.groups
		var cmd tea.Cmd
		m, cmd = m.maybeNotify(true, old, items)
		if cmd != nil {
			next, _ := m.Update(cmd())
			m = next.(Model)
		}
		m.groups = groupUnread(items)
	}
	alice := core.Item{ID: "whatsapp:p:1", Channel: core.ChannelWhatsApp, Unread: true, From: core.Address{Name: "Alice"}, Body: "hola\x1b[31m, ¿vienes?"}
	bob := core.Item{ID: "whatsapp:p:2", Channel: core.ChannelWhatsApp, Unread: true, From: core.Address{Name: "Bob"}, Body: "x"}
	carol := core.Item{ID: "whatsapp:p:3", Channel: core.ChannelWhatsApp, Unread: true, From: core.Address{Name: "Carol"}, Body: "y"}

	arrive(alice)
	now = now.Add(5 * time.Second)
	arrive(alice, bob) // within the interval: coalesced
	now = now.Add(messageNotifyInterval)
	arrive(alice, bob, carol) // fires with bob and carol

	want := []string{"Alice: hola, ¿vienes?", "2 mensajes nuevos"}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Fatalf("bodies = %q, want %q", bodies, want)
	}
	if strings.ContainsRune(bodies[0], '\x1b') {
		t.Fatal("an escape reached the notification")
	}
	if osc.Len() != 0 {
		t.Fatalf("OSC 777 also fired: %q", osc.String())
	}
}

func TestMessageNotifyBodyTruncatesAndNamesChannelForRawIDs(t *testing.T) {
	long := strings.Repeat("palabra ", 20)
	body := messageNotifyBody([]core.Item{{Channel: core.ChannelMatrix, From: core.Address{Name: "!room:example.org"}, Body: long}}, 0)
	if !strings.HasPrefix(body, "Matrix: palabra") || !strings.HasSuffix(body, "…") {
		t.Fatalf("body = %q, want the channel name and a cut text", body)
	}
}

func TestMessageNotifierFailureFlashesOnce(t *testing.T) {
	m := sidebarModel()
	next, _ := m.Update(messageNotifiedMsg{err: errors.New("herdr: notification show: boom")})
	m = next.(Model)
	if flash, _ := m.currentFlash(); !strings.Contains(flash, "no se pudo notificar en herdr") {
		t.Fatalf("flash = %q", flash)
	}
	m = m.withFlash("otra cosa")
	next, _ = m.Update(messageNotifiedMsg{err: errors.New("boom")})
	if flash, _ := next.(Model).currentFlash(); flash != "otra cosa" {
		t.Fatalf("a repeated failure flashed again: %q", flash)
	}
}
