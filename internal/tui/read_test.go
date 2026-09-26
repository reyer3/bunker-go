package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestOpenSelectedItemReadsWithoutReceiptAndReturnsToInbox(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:a:second", Channel: core.ChannelMail, Account: "a",
		Subject: "Hello", Body: "full body", Unread: true,
	}}
	model := NewModel(client)
	model.loaded = true
	model.groups = []inboxGroup{
		{items: []core.Item{{ID: "mail:a:first", Channel: core.ChannelMail}}},
		{items: []core.Item{{ID: "mail:a:second", Channel: core.ChannelMail}}},
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	model = updated.(Model)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("Enter did not request the selected item")
	}
	if client.readCalls != 0 {
		t.Fatal("Read ran synchronously inside Update")
	}
	if !strings.Contains(model.View(), "Loading") {
		t.Fatalf("opening view = %q, want loading state", model.View())
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if client.readCalls != 1 || client.readID != "mail:a:second" || client.readReceipt || client.otherCalls != 1 {
		t.Fatalf("RPC calls: read=%d id=%q receipt=%t other=%d", client.readCalls, client.readID, client.readReceipt, client.otherCalls)
	}
	if view := model.View(); !strings.Contains(view, "full body") || !strings.Contains(view, "Hello") {
		t.Fatalf("read view = %q, want full item", view)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if view := model.View(); !strings.Contains(view, "Mail (") || strings.Contains(view, "full body") {
		t.Fatalf("back view = %q, want inbox", view)
	}
	if model.selected != 1 || client.otherCalls != 1 {
		t.Fatalf("back changed selection or called RPC: selected=%d calls=%d", model.selected, client.otherCalls)
	}
}

func TestReadViewSanitizesBodyAndAttachmentMetadata(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:a:1", Channel: core.ChannelMail, Account: "a",
		From:        core.Address{Name: "Sender\x1b[31m"},
		Body:        "before\x1b[2Jafter\nnext\x1b]0;title\x07line\x00",
		Attachments: []core.Attachment{{Name: "report\x1b[31m.pdf", MIME: "text/plain", Size: 1234, Ref: "opaque"}},
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
	for _, want := range []string{"beforeafter", "nextline", "report.pdf", "text/plain", "1234", "Attachments"} {
		if !strings.Contains(view, want) {
			t.Errorf("view %q lacks %q", view, want)
		}
	}
	for _, bad := range []string{"\x1b", "\x07", "\x00", "[2J", "[31m", "0;title", "opaque"} {
		if strings.Contains(view, bad) {
			t.Errorf("view %q contains %q", view, bad)
		}
	}
	for _, r := range view {
		if unicode.IsControl(r) && r != '\n' {
			t.Errorf("view contains control rune %U", r)
		}
	}
}

func TestReadErrorAndLateResultAfterBack(t *testing.T) {
	client := &inboxClient{readErr: errors.New("fetch failed\x1b[31m")}
	model := NewModel(client)
	model.loaded = true
	model.groups = []inboxGroup{{items: []core.Item{{ID: "mail:a:1", Channel: core.ChannelMail}}}}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not request the selected item")
	}
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if view := model.View(); !strings.Contains(view, "fetch failed") || strings.Contains(view, "\x1b") {
		t.Fatalf("error view = %q", view)
	}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	client.readErr = nil
	client.readResult = core.Item{Body: "late body"}
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not request the selected item again")
	}
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	updated, _ = model.Update(cmd())
	if strings.Contains(updated.(Model).View(), "late body") {
		t.Fatal("late read result reopened a dismissed item")
	}
}

func TestReadViewWrapsNarrowTerminal(t *testing.T) {
	client := &inboxClient{readResult: core.Item{
		ID: "mail:a:1", Subject: "a very long subject", Body: "abcdefghijklmnopqrstuvwxyz",
		Attachments: []core.Attachment{{Name: "very-long-attachment-name.txt", MIME: "application/octet-stream", Size: 42}},
	}}
	model := NewModel(client)
	model.loaded = true
	model.groups = []inboxGroup{{items: []core.Item{{ID: "mail:a:1", Channel: core.ChannelMail}}}}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 12, Height: 8})
	updated, cmd := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not request the selected item")
	}
	updated, _ = updated.(Model).Update(cmd())
	view := updated.(Model).View()
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 12 {
			t.Errorf("line %q exceeds terminal width 12", line)
		}
	}
	if !strings.Contains(strings.ReplaceAll(view, "\n", ""), "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("body was truncated rather than wrapped: %q", view)
	}
}

func TestTerminalTextSanitization(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "CSI", input: "A\x1b[31mB", want: "AB"},
		{name: "OSC with bell", input: "A\x1b]0;title\aB", want: "AB"},
		{name: "OSC with string terminator", input: "A\x1b]0;title\x1b\\B", want: "AB"},
		{name: "DCS", input: "A\x1bPpayload\x1b\\B", want: "AB"},
		{name: "C1 CSI", input: "A\u009b2JB", want: "AB"},
		{name: "incomplete CSI", input: "A\x1b[31", want: "A"},
		{name: "bidi control", input: "A\u202eB", want: "AB"},
		{name: "body lines", input: "A\tB\nC\rD", want: "A B\nCD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTerminalText(tt.input); got != tt.want {
				t.Errorf("sanitizeTerminalText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDismissedReadCannotReplaceReopenedItem(t *testing.T) {
	client := &inboxClient{readResult: core.Item{Body: "old body"}}
	model := NewModel(client)
	model.groups = []inboxGroup{{items: []core.Item{{ID: "same", Channel: core.ChannelMail}}}}
	updated, oldCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	updated, newCmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if oldCmd == nil || newCmd == nil {
		t.Fatal("opening did not return read commands")
	}
	updated, _ = model.Update(oldCmd())
	model = updated.(Model)
	if strings.Contains(model.View(), "old body") {
		t.Fatal("previous request replaced reopened item's loading state")
	}
	client.readResult = core.Item{Body: "new body"}
	updated, _ = model.Update(newCmd())
	if view := updated.(Model).View(); !strings.Contains(view, "new body") {
		t.Fatalf("current result missing: %q", view)
	}
}
