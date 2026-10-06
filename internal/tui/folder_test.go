package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/style"
)

func TestFolderDisplayName(t *testing.T) {
	dovecot := folderLayout{prefix: "INBOX", sep: '.'}
	gmail := folderLayout{sep: '/'}
	cases := []struct {
		folder string
		layout folderLayout
		want   string
	}{
		{"INBOX", dovecot, ""},
		{"inbox", folderLayout{}, ""},
		{"Sent", dovecot, ""},
		{"", dovecot, ""},
		{"INBOX.Archive", dovecot, "Archive"},
		{"INBOX.Clientes.Acme", dovecot, "Clientes/Acme"},
		{"[Gmail]/All Mail", gmail, "Todos"},
		{"[Gmail]/Starred", gmail, "Destacados"},
		{"[Gmail]/Something New", gmail, "Something New"},
		{"[Google Mail]/All Mail", folderLayout{}, "Todos"},
		{"Clientes/Acme", gmail, "Clientes/Acme"},
		{"v1.2 notas", gmail, "v1.2 notas"},
		// No layout (an account missing from the config): the usual
		// INBOX prefix is still recognized.
		{"INBOX.Clientes.Acme", folderLayout{}, "Clientes/Acme"},
		{"INBOX/Archive", folderLayout{}, "Archive"},
		{"Archive", folderLayout{}, "Archive"},
		// A layout with a separator but no prefix still reads as a path.
		{"Clientes.Acme", folderLayout{sep: '.'}, "Clientes/Acme"},
	}
	for _, tc := range cases {
		if got := folderDisplayName(tc.folder, tc.layout); got != tc.want {
			t.Errorf("folderDisplayName(%q, %+v) = %q, want %q", tc.folder, tc.layout, got, tc.want)
		}
	}
}

func TestFolderLayoutsFromConfig(t *testing.T) {
	cfg := config.Config{Accounts: []config.Account{
		{Channel: "mail", Name: "cl", Options: map[string]interface{}{"folder_prefix": "INBOX", "folder_sep": "."}},
		{Channel: "mail", Name: "gm", Options: map[string]interface{}{"gmail": true}},
		{Channel: "whatsapp", Name: "wa", Options: map[string]interface{}{"folder_prefix": "x"}},
	}}
	got := folderLayoutsFromConfig(cfg)
	if got["cl"] != (folderLayout{prefix: "INBOX", sep: '.'}) || got["gm"] != (folderLayout{}) {
		t.Fatalf("layouts = %+v", got)
	}
	if _, ok := got["wa"]; ok {
		t.Fatalf("only mail accounts have folders: %+v", got)
	}
}

func TestFolderTagOnlyForMailAndTruncated(t *testing.T) {
	m := NewModel(nil)
	long := core.Item{Channel: core.ChannelMail, Account: "cl", Meta: map[string]string{"folder": "INBOX.Proyectos.Muy largo de verdad"}}
	tag := m.folderTag(long)
	if runewidth.StringWidth(tag) > folderTagMax || !strings.HasSuffix(tag, "…") {
		t.Fatalf("a long folder is truncated to %d cells: %q", folderTagMax, tag)
	}
	chat := core.Item{Channel: core.ChannelWhatsApp, Meta: map[string]string{"folder": "INBOX.Archive"}}
	if tag := m.folderTag(chat); tag != "" {
		t.Fatalf("only mail shows a folder: %q", tag)
	}
	evil := core.Item{Channel: core.ChannelMail, Meta: map[string]string{"folder": "INBOX.a\x1b[31mb"}}
	if tag := m.folderTag(evil); strings.ContainsRune(tag, '\x1b') {
		t.Fatalf("folder names are sanitized: %q", tag)
	}
}

// TestRowFolderTagKeepsWidth checks that a row with a folder tag never
// exceeds the pane, and gives the tag up before the title at a narrow
// width.
func TestRowFolderTagKeepsWidth(t *testing.T) {
	group := inboxGroup{items: []core.Item{{
		ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl", Thread: "1",
		Subject: "Un asunto bastante largo para probar el ancho", Unread: true, Timestamp: queryAt,
		Meta: map[string]string{"folder": "INBOX.Archive"},
	}}}
	styles := newRowStyles(lipgloss.NewRenderer(io.Discard), style.DefaultPalette())
	for _, width := range []int{30, 40, 60, 100} {
		line1, _ := buildRow(group, false, width, map[core.Channel]string{core.ChannelMail: "M"}, nil, styles, queryAt, "Archive")
		if w := lipgloss.Width(line1); w > width {
			t.Errorf("width %d: row is %d cells: %q", width, w, line1)
		}
		if width >= 60 && !strings.Contains(line1, "Archive") {
			t.Errorf("width %d should show the folder: %q", width, line1)
		}
		if width == 30 && strings.Contains(line1, "Archive") {
			t.Errorf("width 30 should drop the folder for the title: %q", line1)
		}
	}
}

// TestMailThreadFolderHeaderGolden goldens the mail thread header of a
// conversation opened from a subfolder: the folder follows the subject,
// dimmed.
func TestMailThreadFolderHeaderGolden(t *testing.T) {
	at := queryAt
	item := core.Item{
		ID: "mail:cl:9", Channel: core.ChannelMail, Account: "cl", Thread: "t9",
		Subject: "Propuesta anual", From: core.Address{ID: "bob@example.com", Name: "Bob"},
		Timestamp: at, Meta: map[string]string{"folder": "INBOX.Clientes.Acme"},
	}
	client := &inboxClient{threadItems: []core.Item{item}, readResult: core.Item{ID: item.ID, Body: "Adjunto la propuesta."}}
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(client)
	m.render = r
	m.now = func() time.Time { return at }
	m.loaded = true
	m.width, m.height = 60, 16
	m.folderLayouts = map[string]folderLayout{"cl": {prefix: "INBOX", sep: '.'}}

	m, cmd := m.openThread(item)
	updated, fetch := m.Update(cmd())
	m = updated.(Model)
	if fetch != nil {
		updated, _ = m.Update(fetch())
		m = updated.(Model)
	}
	if !strings.Contains(m.View(), "Clientes/Acme") {
		t.Fatalf("the thread header should name the folder:\n%s", m.View())
	}
	teatest.RequireEqualOutput(t, []byte(m.View()))
}
