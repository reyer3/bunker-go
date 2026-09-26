package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestLinkifyURLsWrapsHTTPLinksInOSC8(t *testing.T) {
	got := linkifyURLs("see https://example.org/path for details")
	want := "see \x1b]8;;https://example.org/path\x1b\\https://example.org/path\x1b]8;;\x1b\\ for details"
	if got != want {
		t.Errorf("linkifyURLs = %q, want %q", got, want)
	}
}

func TestLinkifyURLsTrimsTrailingPunctuation(t *testing.T) {
	got := linkifyURLs("(see https://example.org/path).")
	if !strings.Contains(got, "\x1b]8;;https://example.org/path\x1b\\https://example.org/path\x1b]8;;\x1b\\).") {
		t.Errorf("linkifyURLs did not trim trailing punctuation out of the link: %q", got)
	}
}

func TestLinkifyURLsLeavesPlainTextAlone(t *testing.T) {
	if got := linkifyURLs("no links here"); got != "no links here" {
		t.Errorf("linkifyURLs = %q, want unchanged", got)
	}
}

func TestSenderURIPerChannel(t *testing.T) {
	tests := []struct {
		name    string
		channel core.Channel
		addr    string
		want    string
	}{
		{"mail", core.ChannelMail, "alice@example.com", "mailto:alice@example.com"},
		{"matrix", core.ChannelMatrix, "@alice:matrix.org", "https://matrix.to/#/@alice:matrix.org"},
		{"whatsapp numeric jid", core.ChannelWhatsApp, "34600112233@s.whatsapp.net", "https://wa.me/34600112233"},
		{"whatsapp group jid not linkable", core.ChannelWhatsApp, "120363012345678901@g.us", ""},
		{"empty address", core.ChannelMail, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := senderURI(tt.channel, tt.addr); got != tt.want {
				t.Errorf("senderURI(%v, %q) = %q, want %q", tt.channel, tt.addr, got, tt.want)
			}
		})
	}
}

func TestFormatFromLineLinksTheAddressWhenAvailable(t *testing.T) {
	item := core.Item{Channel: core.ChannelMail, From: core.Address{Name: "Alice", ID: "alice@example.com"}}
	got := formatFromLine(item)
	want := "Alice <\x1b]8;;mailto:alice@example.com\x1b\\alice@example.com\x1b]8;;\x1b\\>"
	if got != want {
		t.Errorf("formatFromLine = %q, want %q", got, want)
	}
}

func TestFormatFromLineSanitizesRawContentFirst(t *testing.T) {
	item := core.Item{Channel: core.ChannelMail, From: core.Address{Name: "Alice\x1b[31m", ID: "alice@example.com\x1b[31m"}}
	got := formatFromLine(item)
	if strings.Contains(got, "\x1b[31m") {
		t.Errorf("formatFromLine leaked an unsanitized escape: %q", got)
	}
}

func TestFormatFromLineWithNoAddressShowsNameOnly(t *testing.T) {
	item := core.Item{Channel: core.ChannelMatrix, From: core.Address{Name: "Alice"}}
	if got := formatFromLine(item); got != "Alice" {
		t.Errorf("formatFromLine = %q, want just the name", got)
	}
}

func TestWrapViewSkipsEscapeSequencesWithoutCountingWidth(t *testing.T) {
	link := osc8Link("https://example.org", "https://example.org") // 20 visible cells
	value := "before " + link + " after"
	// Wide enough that the 20-cell visible link itself is never split
	// mid-token by a wrap point (a URL longer than the pane is a
	// separate, real-terminal concern: every common terminal keeps an
	// OSC 8 hyperlink "active" across a wrapped line, so a link that
	// must itself span more than one line is not corrupted either — this
	// test only pins down that the escape bytes are never counted
	// toward the width budget or torn in half).
	wrapped := wrapView(value, 50)
	for _, line := range strings.Split(wrapped, "\n") {
		if w := runewidth.StringWidth(stripANSI(line)); w > 50 {
			t.Errorf("visible line %q is %d cells wide, want <= 50", stripANSI(line), w)
		}
	}
	if !strings.Contains(wrapped, link) {
		t.Errorf("wrapView corrupted the OSC 8 sequence: %q", wrapped)
	}
	if got := runewidth.StringWidth(stripANSI(wrapped)); got != runewidth.StringWidth("before https://example.org after") {
		t.Errorf("visible width changed by the escape sequences: %d", got)
	}
}

// TestDetailViewLinkifiesURLAndSenderButStripsInjectedEscapes is the
// end-to-end G4 check: a real message body/sender flowing through
// Model.View() gets bunker's own OSC 8 links, while any escape sequence
// the message content itself tried to inject is still stripped by
// sanitizeTerminalText — only bunker's own OSC 8 ever reaches the view.
func TestDetailViewLinkifiesURLAndSenderButStripsInjectedEscapes(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:a:1", Channel: core.ChannelMail, Account: "a",
		From: core.Address{Name: "Alice\x1b[31m", ID: "alice@example.com"},
		Body: "see \x1b[31mhttps://example.org/report\x1b[0m\x1b]2;evil\x07 for details",
	}}
	model := NewModel(client)
	model.loaded = true
	model.groups = []inboxGroup{{items: []core.Item{{ID: "mail:a:1", Channel: core.ChannelMail}}}}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not request the selected item")
	}
	updated, _ = updated.(Model).Update(cmd())
	view := updated.(Model).View()

	wantLink := "\x1b]8;;https://example.org/report\x1b\\https://example.org/report\x1b]8;;\x1b\\"
	if !strings.Contains(view, wantLink) {
		t.Fatalf("view %q missing the linkified URL", view)
	}
	wantSenderLink := "\x1b]8;;mailto:alice@example.com\x1b\\alice@example.com\x1b]8;;\x1b\\"
	if !strings.Contains(view, wantSenderLink) {
		t.Fatalf("view %q missing the linkified sender address", view)
	}
	for _, injected := range []string{"\x1b[31m", "\x1b[0m", "\x1b]2;evil", "\x07"} {
		if strings.Contains(view, injected) {
			t.Errorf("view %q leaked message-injected escape %q", view, injected)
		}
	}
}
