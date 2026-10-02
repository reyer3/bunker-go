package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestWrapWords(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"fits", "hola mundo", 20, []string{"hola mundo"}},
		{"breaks at spaces", "uno dos tres cuatro", 8, []string{"uno dos", "tres", "cuatro"}},
		{"hard-splits a long word on its own line", "ver https://example.com/abcdef", 10, []string{"ver", "https://ex", "ample.com/", "abcdef"}},
		{"wide runes", "日本語のテキスト", 6, []string{"日本語", "のテキ", "スト"}},
		{"empty", "", 10, []string{""}},
		{"no leading space after an exact split", "abcdefghij klm", 5, []string{"abcde", "fghij", "klm"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapWords(tc.text, tc.width)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("wrapWords(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
			for _, line := range got {
				if w := runewidth.StringWidth(line); w > tc.width {
					t.Fatalf("line %q is %d cells, wider than %d", line, w, tc.width)
				}
			}
		})
	}
}

// TestChatBubbleWrapsLongMessages pins the fix for long messages cut with
// "…" in a narrow pane: the whole body shows, wrapped inside the bubble.
func TestChatBubbleWrapsLongMessages(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.Ascii)
	body := "Este es un mensaje bastante largo que no cabe en una sola línea del panel"
	lines := chatBubbleLines(r, core.Item{Body: body, Timestamp: now}, 32, false, now, false, "", nil)

	var words []string
	for _, line := range lines[:len(lines)-1] { // the last line is the time
		if strings.Contains(line, "…") {
			t.Fatalf("bubble line %q is truncated", line)
		}
		words = append(words, strings.Fields(line)...)
	}
	if got := strings.Join(words, " "); got != body {
		t.Fatalf("bubble text = %q, want the whole body %q", got, body)
	}
	if len(lines) < 4 {
		t.Fatalf("bubble has %d lines, want the body wrapped over several", len(lines))
	}
}

// TestChatComposerFollowsResize pins the composer following the pane's
// width: a "bunker open" pane builds it before the first size arrives.
func TestChatComposerFollowsResize(t *testing.T) {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model.width = 0
	model, _ = openChat(model)
	if !model.chatMode {
		t.Fatal("the chat did not open")
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 70, Height: 20})
	model = updated.(Model)
	if got, want := model.composer.Width(), chatComposerWidth(70); got != want {
		t.Fatalf("composer width = %d, want %d after the resize", got, want)
	}
	for _, line := range strings.Split(model.composerBox(), "\n") {
		if w := lipgloss.Width(line); w != 70 {
			t.Fatalf("composer box line is %d cells wide, want the pane's 70", w)
		}
	}
}

// TestResizeWithoutComposerDoesNotPanic pins that a resize in the inbox,
// where the composer was never built, leaves it alone.
func TestResizeWithoutComposerDoesNotPanic(t *testing.T) {
	model := NewModel(&replyClient{}).withGlyphs(nil)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 50, Height: 20})
	if updated.(Model).width != 50 {
		t.Fatal("the resize was not applied")
	}
}
