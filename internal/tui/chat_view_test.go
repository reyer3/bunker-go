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
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestChatViewFitsPaneHeightWithManyMessages is the parent's reported live
// bug: a busy conversation (~40 messages) made the chat view render
// TALLER than the pane, pushing the header and day pill off the top of a
// real scrolling terminal. The view must obey the same fitInbox height
// contract as the inbox: total rendered lines <= pane height, the header
// stays the first line, the newest message is visible by default
// (bottom-aligned, scrolled to the newest on open), and the composer
// stays visible.
func TestChatViewFitsPaneHeightWithManyMessages(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model.width = 62
	model.height = 20
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	items := make([]core.Item, 0, 40)
	for i := 0; i < 40; i++ {
		items = append(items, core.Item{
			ID: fmt.Sprintf("whatsapp:personal:%d", i), Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			FromMe: i%2 == 1, From: core.Address{Name: "Alice"}, Body: fmt.Sprintf("mensaje numero %d", i),
			Timestamp: now.Add(time.Duration(i) * time.Minute),
		})
	}
	msg.items = items
	updated, _ := model.Update(msg)
	model = updated.(Model)

	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) > model.height {
		t.Fatalf("chat view has %d lines, want <= pane height %d", len(lines), model.height)
	}
	if !strings.Contains(lines[0], "Alice") {
		t.Fatalf("line 0 = %q, want the header (contact name) to stay the first, always-visible line", lines[0])
	}
	if !strings.Contains(view, "mensaje numero 39") {
		t.Fatalf("view = %q, want the newest message (39) visible by default", view)
	}
	if !strings.Contains(view, "Escribe un mensaje") {
		t.Fatalf("view = %q, want the composer placeholder still visible", view)
	}
}

// TestChatBubbleWidthCapsAtAbout75PercentOfWidth pins K7's exact width
// math (conversation-view.md: "max ~75% of the width"), including its
// narrow-terminal degrade to the full width.
func TestChatBubbleWidthCapsAtAbout75PercentOfWidth(t *testing.T) {
	cases := []struct{ width, want int }{
		{0, 40},
		{-5, 40},
		{10, 10},
		{20, 20},
		{40, 30},
		{100, 75},
		{200, 150},
	}
	for _, c := range cases {
		if got := chatBubbleWidth(c.width); got != c.want {
			t.Errorf("chatBubbleWidth(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

// TestChatBubbleAlignmentOwnRightIncomingLeft pins the alignment math: an
// incoming bubble's content starts at column 0, an own bubble's content
// is right-padded so it ends flush with the terminal's right edge. Uses
// an Ascii color profile so the rendered bytes are exactly the padded
// plain text (no ANSI to strip), making the exact column position
// directly assertable.
func TestChatBubbleAlignmentOwnRightIncomingLeft(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	width := 40
	bubbleWidth := chatBubbleWidth(width)

	incoming := chatBubbleLines(r, core.Item{Body: "hi", Timestamp: now}, width, false, now, false, "")
	if !strings.HasPrefix(incoming[0], "hi") {
		t.Fatalf("incoming bubble body line = %q, want it flush with the left edge", incoming[0])
	}

	own := chatBubbleLines(r, core.Item{Body: "hi", FromMe: true, Channel: core.ChannelWhatsApp, Timestamp: now}, width, false, now, false, "")
	wantPad := strings.Repeat(" ", width-bubbleWidth)
	if !strings.HasPrefix(own[0], wantPad+"hi") {
		t.Fatalf("own bubble body line = %q, want %d leading spaces then the text (right edge)", own[0], width-bubbleWidth)
	}
	if runewidth.StringWidth(own[0]) != width {
		t.Fatalf("own bubble line width = %d, want the full terminal width %d", runewidth.StringWidth(own[0]), width)
	}
}

// TestChatBubbleBackgroundColorsDifferByOwnershipAndChannel forces a
// TrueColor profile so the background/foreground escapes actually render,
// and checks the incoming bubble, an own WhatsApp bubble and an own
// Matrix bubble are all rendered with distinct styling.
func TestChatBubbleBackgroundColorsDifferByOwnershipAndChannel(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	now := time.Now()

	incoming := chatBubbleLines(r, core.Item{Body: "hi", Timestamp: now}, 40, false, now, false, "")[0]
	ownWA := chatBubbleLines(r, core.Item{Body: "hi", FromMe: true, Channel: core.ChannelWhatsApp, Timestamp: now}, 40, false, now, false, "")[0]
	ownMatrix := chatBubbleLines(r, core.Item{Body: "hi", FromMe: true, Channel: core.ChannelMatrix, Timestamp: now}, 40, false, now, false, "")[0]

	if incoming == ownWA {
		t.Fatal("incoming and own bubbles rendered identically; want distinct backgrounds")
	}
	if ownWA == ownMatrix {
		t.Fatal("own WhatsApp and own Matrix bubbles rendered identically; want distinct backgrounds")
	}
	if stripANSI(incoming) == incoming {
		t.Fatal("incoming bubble carries no styling at all under a forced TrueColor profile")
	}
}

// TestChatDayPillBackgroundWrapsOnlyLabelNotFullWidth pins the "centered
// dim pill" shape: the label is styled, the surrounding padding on both
// sides is plain — a pill, not a full-width bar.
func TestChatDayPillBackgroundWrapsOnlyLabelNotFullWidth(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	model := Model{width: 40, render: r}
	line := model.dayPillLine("hoy")

	stripped := stripANSI(line)
	if runewidth.StringWidth(stripped) != 40 {
		t.Fatalf("pill line width = %d, want 40", runewidth.StringWidth(stripped))
	}
	if !strings.Contains(stripped, "hoy") {
		t.Fatalf("pill line = %q, want the label", stripped)
	}
	if strings.HasPrefix(line, "\x1b") {
		t.Fatalf("pill line = %q, want plain (unstyled) leading padding before the label's own escape", line)
	}
	if stripANSI(line) == line {
		t.Fatal("day pill carries no styling at all under a forced TrueColor profile")
	}
}

// TestChatHeaderShowsNameAndPresenceOnSeparateLines pins K7's two-line
// header: the contact/group name (bold) on line 1, live presence (dim) on
// line 2 when known.
func TestChatHeaderShowsNameAndPresenceOnSeparateLines(t *testing.T) {
	client := &replyClient{}
	client.presenceResult = core.Presence{State: "typing"}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	lines := strings.Split(model.View(), "\n")
	if len(lines) < 2 {
		t.Fatalf("chat view has %d lines, want at least a name and a presence line", len(lines))
	}
	if !strings.Contains(lines[0], "Alice") {
		t.Fatalf("header line 1 = %q, want the contact name", lines[0])
	}
	if !strings.Contains(lines[1], "escribiendo…") {
		t.Fatalf("header line 2 = %q, want the presence text", lines[1])
	}
}

// TestChatComposerGrowsUpToMaxThenStopsGrowing pins the docked composer's
// growth rule: it starts at 1 line and grows with the draft's own line
// count up to chatComposerMaxHeight (3), never beyond.
func TestChatComposerGrowsUpToMaxThenStopsGrowing(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	if h := model.composer.Height(); h != 1 {
		t.Fatalf("initial composer height = %d, want 1", h)
	}
	for i := 2; i <= 5; i++ {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
		model = updated.(Model)
		model = typeRunes(model, "x")
		want := i
		if want > chatComposerMaxHeight {
			want = chatComposerMaxHeight
		}
		if h := model.composer.Height(); h != want {
			t.Fatalf("after %d draft lines, composer height = %d, want %d", i, h, want)
		}
	}
}

// TestChatViewDegradesGracefullyWithNoColor mirrors G1's NO_COLOR
// discipline for the K7 chat view: an Ascii profile must never leak an
// ANSI escape.
func TestChatViewDegradesGracefullyWithNoColor(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	model.render = r
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	view := model.View()
	if strings.Contains(view, "\x1b") {
		t.Errorf("Ascii/NO_COLOR profile leaked an ANSI escape in the chat view:\n%q", view)
	}
}

// TestClampScrollBoundsToValidRange pins the scroll-clamping math the K7
// height fix's window relies on: never negative, never past showing a
// full budget-sized window (maxScroll = total-budget), and a budget
// exceeding total clamps to 0 rather than going negative.
func TestClampScrollBoundsToValidRange(t *testing.T) {
	cases := []struct{ scroll, total, budget, want int }{
		{-5, 100, 20, 0},
		{0, 100, 20, 0},
		{50, 100, 20, 50},
		{90, 100, 20, 80},
		{1000, 100, 20, 80},
		{5, 10, 20, 0},
	}
	for _, c := range cases {
		if got := clampScroll(c.scroll, c.total, c.budget); got != c.want {
			t.Errorf("clampScroll(%d, %d, %d) = %d, want %d", c.scroll, c.total, c.budget, got, c.want)
		}
	}
}

// TestWindowTailReturnsBottomAlignedSlice pins windowTail's own contract
// directly: scroll=0 shows the tail (newest budget lines), a positive
// scroll shifts the window earlier, and an over-large scroll clamps
// instead of ever slicing out of range (which would panic).
func TestWindowTailReturnsBottomAlignedSlice(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	window, scroll := windowTail(lines, 0, 3)
	if scroll != 0 || strings.Join(window, ",") != "c,d,e" {
		t.Fatalf("windowTail(scroll=0) = %v (scroll=%d), want [c d e] scroll=0", window, scroll)
	}
	window, scroll = windowTail(lines, 2, 3)
	if scroll != 2 || strings.Join(window, ",") != "a,b,c" {
		t.Fatalf("windowTail(scroll=2) = %v (scroll=%d), want [a b c] scroll=2", window, scroll)
	}
	window, scroll = windowTail(lines, 100, 3)
	if scroll != 2 || strings.Join(window, ",") != "a,b,c" {
		t.Fatalf("windowTail(scroll=100) = %v (scroll=%d), want clamped to [a b c] scroll=2", window, scroll)
	}
}

// chatModelWithManyMessages builds a chat already loaded with n messages,
// at the given pane size, for the scrolling tests below.
func chatModelWithManyMessages(n, width, height int) (Model, *replyClient) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.now = func() time.Time { return now }
	model.width, model.height = width, height
	model, cmd := openChat(model)
	msg := cmd().(chatThreadLoadedMsg)
	items := make([]core.Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, core.Item{
			ID: fmt.Sprintf("whatsapp:personal:%d", i), Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			FromMe: i%2 == 1, From: core.Address{Name: "Alice"}, Body: fmt.Sprintf("mensaje numero %d", i),
			Timestamp: now.Add(time.Duration(i) * time.Minute),
		})
	}
	msg.items = items
	updated, _ := model.Update(msg)
	return updated.(Model), client
}

// TestChatPgUpScrollsThenLoadsOlderPage pins the scroll/pagination
// contract: PgUp first scrolls up through already-loaded messages
// (revealing older ones, hiding the newest), and once scrolled as far up
// as the loaded content allows, a further PgUp requests an older page —
// the same "reaching the top loads more" rule the plain "Up" key already
// had (conversation-view.md: "PgUp/mouse wheel scroll history, loading
// older pages").
func TestChatPgUpScrollsThenLoadsOlderPage(t *testing.T) {
	model, client := chatModelWithManyMessages(40, 62, 20)
	if !strings.Contains(model.View(), "mensaje numero 39") {
		t.Fatalf("view = %q, want the newest message visible before scrolling", model.View())
	}

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if strings.Contains(model.View(), "mensaje numero 39") {
		t.Fatalf("view after PgUp = %q, want the newest message scrolled out of view", model.View())
	}
	if len(client.threadCalls) != 1 {
		t.Fatalf("thread calls after one PgUp = %d, want 1 (only the initial open, no pagination yet)", len(client.threadCalls))
	}

	// Keep paging up until the top of the loaded thread is reached and an
	// older page is requested instead of scrolling further.
	var loadedOlder bool
	for i := 0; i < 20; i++ {
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
		model = updated.(Model)
		if cmd != nil {
			resultMsg := cmd()
			if _, ok := resultMsg.(chatThreadLoadedMsg); ok {
				loadedOlder = true
				break
			}
		}
	}
	if !loadedOlder {
		t.Fatal("repeatedly paging up never requested an older page")
	}
	if len(client.threadCalls) != 2 {
		t.Fatalf("thread calls = %d, want 2 (the initial open plus one older-page request)", len(client.threadCalls))
	}
}

// TestChatPgDownScrollsBackTowardNewest pins PgDown as PgUp's inverse.
func TestChatPgDownScrollsBackTowardNewest(t *testing.T) {
	model, _ := chatModelWithManyMessages(40, 62, 20)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	model = updated.(Model)
	if strings.Contains(model.View(), "mensaje numero 39") {
		t.Fatal("expected the newest message to be scrolled out of view after PgUp")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	model = updated.(Model)
	if !strings.Contains(model.View(), "mensaje numero 39") {
		t.Fatalf("view after PgDown = %q, want the newest message visible again", model.View())
	}
}

// TestChatMouseWheelScrollsChatBody pins the mouse wheel as a smaller-step
// scroll (chatWheelScroll lines) over the same body window PgUp/PgDown
// use.
func TestChatMouseWheelScrollsChatBody(t *testing.T) {
	model, _ := chatModelWithManyMessages(40, 62, 20)
	updated, _ := model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	model = updated.(Model)
	if model.chatScroll != chatWheelScroll {
		t.Fatalf("chatScroll after one wheel-up = %d, want %d", model.chatScroll, chatWheelScroll)
	}
	updated, _ = model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	model = updated.(Model)
	if model.chatScroll != 0 {
		t.Fatalf("chatScroll after wheel-up then wheel-down = %d, want 0", model.chatScroll)
	}
}

// TestWalkthroughChatViewGolden goldens the K7 chat view's final rendered
// screen (not the frame stream — see teatest_walkthrough_test.go's
// sessionRecorder doc comment) at a fixed clock and a forced TrueColor
// profile, so the bubble/composer/day-pill styling is locked into the
// golden bytes for review, not just asserted piecemeal.
func TestWalkthroughChatViewGolden(t *testing.T) {
	at := time.Date(2026, 9, 26, 10, 42, 0, 0, time.UTC)
	client := &replyClient{}
	client.items = []core.Item{{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511999999999@s.whatsapp.net", From: core.Address{ID: "5511999999999@s.whatsapp.net", Name: "Alice"},
		Unread: true, Timestamp: at,
	}}
	client.threadItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: false, From: core.Address{Name: "Alice"}, Body: "hola, ¿cómo vas?", Timestamp: at.Add(-time.Hour)},
		{ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: true, Body: "todo bien, gracias", Timestamp: at},
	}
	client.presenceResult = core.Presence{State: "online"}

	model := NewModel(client)
	model.now = func() time.Time { return at }
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	model.render = r

	updated, cmd := model.Update(model.Init()())
	m := updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 62, Height: 24})
	m = updated.(Model)
	_ = cmd

	updated, openCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(openCmd())
	m = updated.(Model)

	teatest.RequireEqualOutput(t, []byte(m.View()))
}

// TestWalkthroughChatOptimisticBubbleGolden goldens K10's optimistic own
// bubble: right after confirming a send (before the real, possibly-slow
// send even returns), the composer is already cleared, the draft renders
// as a dim own bubble marked "enviando…", and the tail shows
// "Enviando..." instead of the confirm prompt — fixed clock and forced
// TrueColor profile, like TestWalkthroughChatViewGolden.
func TestWalkthroughChatOptimisticBubbleGolden(t *testing.T) {
	at := time.Date(2026, 9, 26, 10, 42, 0, 0, time.UTC)
	client := &replyClient{previewOut: core.Plan{Recipients: []string{"5511999999999@s.whatsapp.net"}}}
	client.items = []core.Item{{
		ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal",
		Thread: "5511999999999@s.whatsapp.net", From: core.Address{ID: "5511999999999@s.whatsapp.net", Name: "Alice"},
		Unread: true, Timestamp: at,
	}}
	client.threadItems = []core.Item{
		{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t", FromMe: false, From: core.Address{Name: "Alice"}, Body: "hola, ¿cómo vas?", Timestamp: at.Add(-time.Hour)},
	}
	client.presenceResult = core.Presence{State: "online"}

	model := NewModel(client)
	model.now = func() time.Time { return at }
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	model.render = r

	updated, cmd := model.Update(model.Init()())
	m := updated.(Model)
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 62, Height: 24})
	m = updated.(Model)
	_ = cmd

	updated, openCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(openCmd())
	m = updated.(Model)

	m = typeRunes(m, "todo bien")
	updated, previewCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(previewCmd())
	m = updated.(Model)

	// The real send is never resolved in this test: only the synchronous
	// optimistic state that Update sets on confirm, before any command
	// even runs, is under test here.
	client.block = make(chan struct{})
	updated, sendCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	_ = sendCmd

	teatest.RequireEqualOutput(t, []byte(m.View()))
}
