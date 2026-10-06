package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestParseMailto(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want MailDraft
	}{
		{"address only", "mailto:ana@example.com", MailDraft{To: "ana@example.com"}},
		{"scheme is case-insensitive", "MAILTO:ana@example.com", MailDraft{To: "ana@example.com"}},
		{"several addresses and to hfield", "mailto:ana@example.com,bob@example.com?to=carl@example.com",
			MailDraft{To: "ana@example.com, bob@example.com, carl@example.com"}},
		{"percent-encoded address", "mailto:ana%2Bnews@example.com", MailDraft{To: "ana+news@example.com"}},
		{"cc subject and body", "mailto:ana@example.com?cc=bob@example.com&subject=Hola%20mundo&body=l%C3%ADnea%201%0D%0Al%C3%ADnea%202",
			MailDraft{To: "ana@example.com", Cc: "bob@example.com", Subject: "Hola mundo", Body: "línea 1\nlínea 2"}},
		{"plus is literal, not a space", "mailto:ana@example.com?subject=1+1", MailDraft{To: "ana@example.com", Subject: "1+1"}},
		{"hfield names are case-insensitive", "mailto:?TO=ana@example.com&Subject=x", MailDraft{To: "ana@example.com", Subject: "x"}},
		{"unknown hfields are ignored", "mailto:ana@example.com?in-reply-to=%3Cid%40x%3E&x-foo=1", MailDraft{To: "ana@example.com"}},
		{"repeated subject keeps the first", "mailto:ana@example.com?subject=a&subject=b", MailDraft{To: "ana@example.com", Subject: "a"}},
		{"no recipient", "mailto:?subject=x", MailDraft{Subject: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseMailto(tc.raw)
			if err != nil {
				t.Fatalf("ParseMailto(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("ParseMailto(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseMailtoRejectsBadInput(t *testing.T) {
	cases := []struct {
		name, raw, wantErr string
	}{
		{"other scheme", "https://example.com", "not a mailto"},
		{"empty", "", "not a mailto"},
		{"bad escape", "mailto:ana%zz@example.com", "invalid"},
		{"newline in address", "mailto:ana@example.com%0Abcc:eve@example.com", "control character"},
		{"newline in subject", "mailto:ana@example.com?subject=a%0D%0ABcc:%20eve@example.com", "control character"},
		{"bcc cannot be honored", "mailto:ana@example.com?bcc=eve@example.com", "bcc"},
		{"too long", "mailto:" + strings.Repeat("a", maxMailtoLen), "too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMailto(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ParseMailto(%q) error = %v, want it to mention %q", tc.raw, err, tc.wantErr)
			}
		})
	}
}

// composeModel is a "bunker compose" Model with its editor opened, the
// way Run starts it.
func composeModel(client Client, d MailDraft) Model {
	return NewModel(client, WithMailDraft("work", d)).startMailDraft()
}

func TestMailDraftOpensTheNewMailEditor(t *testing.T) {
	d := MailDraft{To: "ana@example.com", Cc: "bob@example.com", Subject: "Hola", Body: "texto"}
	m := composeModel(&replyClient{}, d)
	if !m.mailComposing || m.mailAction != "new" {
		t.Fatalf("mailComposing=%v action=%q, want the new mail editor", m.mailComposing, m.mailAction)
	}
	if m.mailChannel != core.ChannelMail || m.mailAccount != "work" {
		t.Errorf("channel/account = %s/%s, want mail/work", m.mailChannel, m.mailAccount)
	}
	if m.mailTo.Value() != d.To || m.mailCc.Value() != d.Cc || m.mailSubject.Value() != d.Subject || m.composer.Value() != d.Body {
		t.Errorf("editor = %q/%q/%q/%q, want the draft", m.mailTo.Value(), m.mailCc.Value(), m.mailSubject.Value(), m.composer.Value())
	}
	if m.mailFocus != 3 {
		t.Errorf("focus = %d, want the body (3) when To and Subject are filled", m.mailFocus)
	}
	if m.polling {
		t.Error("a compose pane polls the inbox; want it not to")
	}
	if cmd := m.Init(); cmd != nil {
		t.Error("Init loads the inbox for a compose pane; want nothing")
	}
}

func TestMailDraftFocusesTheFirstEmptyField(t *testing.T) {
	if m := composeModel(&replyClient{}, MailDraft{}); m.mailFocus != 0 {
		t.Errorf("focus = %d, want To (0) with no recipient", m.mailFocus)
	}
	if m := composeModel(&replyClient{}, MailDraft{To: "ana@example.com"}); m.mailFocus != 2 {
		t.Errorf("focus = %d, want Subject (2) with a recipient and no subject", m.mailFocus)
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestMailDraftPaneQuitsWhenTheEditorCloses(t *testing.T) {
	m := composeModel(&replyClient{}, MailDraft{To: "ana@example.com"})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !isQuit(cmd) {
		t.Error("Esc in a compose pane did not quit")
	}

	m = composeModel(&replyClient{}, MailDraft{To: "ana@example.com"})
	m.mailSending = true
	_, cmd = m.Update(mailSentMsg{token: m.mailToken})
	if !isQuit(cmd) {
		t.Error("a sent message did not quit the compose pane")
	}
}

func TestEditorEscWithoutDraftStaysInTheTUI(t *testing.T) {
	m := NewModel(&replyClient{}).openNewMail(core.Contact{Channel: core.ChannelMail, Account: "work", Address: "ana@example.com"})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if isQuit(cmd) {
		t.Error("Esc in the full TUI's editor quit; want it to return to the inbox")
	}
}
