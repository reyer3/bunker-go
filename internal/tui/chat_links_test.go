package tui

import (
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

var osc8Pattern = regexp.MustCompile("\x1b\\]8;;[^\x1b]*\x1b\\\\")

func stripOSC8(s string) string { return osc8Pattern.ReplaceAllString(s, "") }

func linkOpen(url string) string { return "\x1b]8;;" + url + "\x1b\\" }

func plainBubble(t *testing.T, body string, width int) []string {
	t.Helper()
	r := lipgloss.NewRenderer(io.Discard)
	item := core.Item{ID: "whatsapp:personal:x", Channel: core.ChannelWhatsApp, Body: body, Timestamp: uxNow}
	return chatBubbleLines(r, item, width, false, uxNow, false, "", nil)
}

func TestChatBubbleLinksURLs(t *testing.T) {
	lines := plainBubble(t, "mira https://example.org/a. ok", 60)
	joined := strings.Join(lines, "\n")
	want := linkOpen("https://example.org/a") + "https://example.org/a" + linkOpen("")
	if !strings.Contains(joined, want) {
		t.Fatalf("the URL is not an OSC 8 link (trailing dot excluded):\n%q", joined)
	}
	if !strings.Contains(stripOSC8(joined), "mira https://example.org/a. ok") {
		t.Fatalf("linking changed the visible text:\n%q", stripOSC8(joined))
	}
}

func TestChatBubbleLinksAWrappedURLWholeOnEveryLine(t *testing.T) {
	url := "https://example.org/" + strings.Repeat("x", 70)
	width := 60
	lines := plainBubble(t, "ver "+url+" ya", width)
	bubbleWidth := chatBubbleWidth(width)
	linked := 0
	for _, l := range lines {
		if w := runewidth.StringWidth(stripOSC8(l)); w != bubbleWidth {
			t.Errorf("line %q is %d cells, want the bubble width %d", stripOSC8(l), w, bubbleWidth)
		}
		if strings.Contains(l, linkOpen(url)) {
			linked++
		}
		if strings.Contains(l, "\x1b]8;;https://example.org/x") && !strings.Contains(l, linkOpen(url)) {
			t.Errorf("a wrapped chunk links a partial URL: %q", l)
		}
	}
	if linked < 2 {
		t.Fatalf("the URL spans several lines but only %d link to it:\n%q", linked, strings.Join(lines, "\n"))
	}
}

func TestChatBubbleWithoutURLsIsUnchanged(t *testing.T) {
	for _, l := range plainBubble(t, "sin enlaces aquí", 60) {
		if strings.Contains(l, "\x1b]8;;") {
			t.Fatalf("a plain bubble got a link: %q", l)
		}
	}
}

func TestMessageURLs(t *testing.T) {
	tests := []struct {
		name string
		item core.Item
		want []string
	}{
		{"none", core.Item{Body: "hola"}, nil},
		{"one, punctuation trimmed", core.Item{Body: "ver https://example.org/a."}, []string{"https://example.org/a"}},
		{"several, deduplicated in order", core.Item{Body: "https://example.org/b y http://example.org/c\notra vez https://example.org/b"},
			[]string{"https://example.org/b", "http://example.org/c"}},
		{"deleted message", core.Item{Body: "https://example.org/a", Deleted: true}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := messageURLs(tt.item)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("messageURLs = %v, want %v", got, tt.want)
			}
		})
	}
}

// linkChat opens a chat whose messages carry links, recording what the
// opener is asked to open.
func linkChat(t *testing.T) (Model, *[]string) {
	t.Helper()
	model, _ := uxChat(t, &replyClient{})
	msg := chatThreadLoadedMsg{token: model.chatToken, items: []core.Item{
		{ID: "whatsapp:personal:a", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Body: "uno https://example.org/one", Timestamp: uxNow},
		{ID: "whatsapp:personal:b", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			From: core.Address{Name: "Alice"}, Body: "sin enlaces", Timestamp: uxNow},
		{ID: "whatsapp:personal:c", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "t",
			FromMe: true, Body: "https://example.org/x y https://example.org/y", Timestamp: uxNow.Add(time.Minute)},
	}}
	updated, _ := model.Update(msg)
	model = updated.(Model)
	opened := &[]string{}
	model.openURL = func(u string) error { *opened = append(*opened, u); return nil }
	return model, opened
}

func runLinkCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	updated, _ := m.Update(cmd())
	return updated.(Model)
}

func TestChatViewShowsLinkedURLs(t *testing.T) {
	model, _ := linkChat(t)
	if !strings.Contains(model.View(), linkOpen("https://example.org/one")) {
		t.Fatalf("the chat view does not link the URL:\n%q", model.View())
	}
}

func TestAltLOpensTheSelectedMessagesOnlyLink(t *testing.T) {
	model, opened := linkChat(t)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	model, cmd := uxPress(t, model, keyAlt('l'))
	model = runLinkCmd(t, model, cmd)
	if len(*opened) != 1 || (*opened)[0] != "https://example.org/one" {
		t.Fatalf("opened %v, want the selected message's link", *opened)
	}
	if model.chatLinks != nil {
		t.Fatal("a single link should open without a picker")
	}
}

func TestAltLWithoutLinksSaysSo(t *testing.T) {
	model, opened := linkChat(t)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp) // "sin enlaces"
	model, cmd := uxPress(t, model, keyAlt('l'))
	if cmd != nil || len(*opened) != 0 {
		t.Fatal("a message without links opened something")
	}
	if !strings.Contains(model.View(), "no tiene enlaces") {
		t.Fatalf("no notice about the missing link:\n%s", model.View())
	}
}

func TestAltLPicksAmongSeveralLinks(t *testing.T) {
	// With no selection the newest message is used, as with Alt+Y.
	model, opened := linkChat(t)
	model, cmd := uxPress(t, model, keyAlt('l'))
	if cmd != nil || len(*opened) != 0 {
		t.Fatal("several links should ask first")
	}
	view := model.View()
	if !strings.Contains(view, "1 https://example.org/x") || !strings.Contains(view, "2 https://example.org/y") {
		t.Fatalf("the picker does not list the links:\n%s", view)
	}
	// Typed keys never reach the draft while picking.
	model, _ = uxPress(t, model, runes("z"))
	if model.composer.Value() != "" {
		t.Fatalf("a key reached the composer: %q", model.composer.Value())
	}
	model, cmd = uxPress(t, model, runes("2"))
	model = runLinkCmd(t, model, cmd)
	if len(*opened) != 1 || (*opened)[0] != "https://example.org/y" {
		t.Fatalf("opened %v, want the second link", *opened)
	}
	if model.chatLinks != nil {
		t.Fatal("the picker should close once a link opens")
	}

	// Arrows and Enter pick too.
	model, _ = uxPress(t, model, keyAlt('l'))
	model, _ = uxPress(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model, cmd = uxPress(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = runLinkCmd(t, model, cmd)
	if len(*opened) != 2 || (*opened)[1] != "https://example.org/y" {
		t.Fatalf("opened %v, want the second link via ↓ ↵", *opened)
	}

	// Esc cancels without leaving the chat.
	model, _ = uxPress(t, model, keyAlt('l'))
	model, cmd = uxPress(t, model, escKey)
	if cmd != nil || model.chatLinks != nil || !model.chatMode || len(*opened) != 2 {
		t.Fatalf("Esc should only close the picker (links=%v chat=%v)", model.chatLinks, model.chatMode)
	}
}

func TestOpenLinkFailureIsShown(t *testing.T) {
	model, _ := linkChat(t)
	model.openURL = func(string) error { return io.ErrUnexpectedEOF }
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	model, _ = uxPress(t, model, altUp)
	model, cmd := uxPress(t, model, keyAlt('l'))
	model = runLinkCmd(t, model, cmd)
	if !strings.Contains(model.View(), "No se pudo abrir el enlace") {
		t.Fatalf("the opener's failure is not shown:\n%s", model.View())
	}
}

func TestHelpListsAltL(t *testing.T) {
	model, _ := openedChat(t)
	model.height = 0
	if help := model.openHelp().helpView(); !strings.Contains(help, "Alt+L") {
		t.Fatal("help does not mention Alt+L")
	}
}
