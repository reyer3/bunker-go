package mail

import (
	"strings"
	"testing"
)

func TestFolderMapResolve(t *testing.T) {
	m := NewFolderMap('.', "INBOX")
	m.SetSpecialUse(SpecialUseSent, "INBOX.Sent")

	tests := []struct {
		name     string
		friendly string
		want     string
	}{
		{"inbox stays inbox", "INBOX", "INBOX"},
		{"plain folder joins prefix and separator", "Archives", "INBOX.Archives"},
		{"plain folder joins prefix and separator, another", "Spam", "INBOX.Spam"},
		{"special-use sent is discovered, not guessed", "Sent", "INBOX.Sent"},
		{"already-prefixed name is left alone", "INBOX.Archives", "INBOX.Archives"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.Resolve(tt.friendly); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.friendly, got, tt.want)
			}
		})
	}
}

func TestFolderMapResolveNoPrefix(t *testing.T) {
	// Gmail-style: no prefix, '/' separator, Sent auto-saved so no
	// special-use mapping is registered for it.
	m := NewFolderMap('/', "")
	if got := m.Resolve("Archives"); got != "Archives" {
		t.Errorf("Resolve(%q) = %q, want %q", "Archives", got, "Archives")
	}
}

// TestFolderMapResolveArchive covers issue #54: "Archive"/"Archives" map
// to \Archive when advertised, and otherwise to whichever archive mailbox
// LIST reported, under the configured prefix and separator.
func TestFolderMapResolveArchive(t *testing.T) {
	tests := []struct {
		name       string
		sep        byte
		prefix     string
		specialUse map[string]string
		mailboxes  []string // nil: no LIST available
		friendly   string
		want       string
	}{
		{
			name:       "gmail-like uses the \\Archive attribute",
			sep:        '/',
			specialUse: map[string]string{SpecialUseArchive: "[Gmail]/Archive"},
			mailboxes:  []string{"INBOX", "[Gmail]/Archive", "[Gmail]/Sent Mail"},
			friendly:   "Archives",
			want:       "[Gmail]/Archive",
		},
		{
			name:      "dovecot-like INBOX.Archive without attribute",
			sep:       '.',
			prefix:    "INBOX",
			mailboxes: []string{"INBOX", "INBOX.Archive", "INBOX.Sent"},
			friendly:  "Archives",
			want:      "INBOX.Archive",
		},
		{
			name:      "dovecot-like INBOX.Archives without attribute",
			sep:       '.',
			prefix:    "INBOX",
			mailboxes: []string{"INBOX", "INBOX.Archives", "INBOX.Sent"},
			friendly:  "Archive",
			want:      "INBOX.Archives",
		},
		{
			name:      "requested spelling wins when both exist",
			sep:       '.',
			prefix:    "INBOX",
			mailboxes: []string{"INBOX", "INBOX.Archive", "INBOX.Archives"},
			friendly:  "Archives",
			want:      "INBOX.Archives",
		},
		{
			name:      "unprefixed archive on a prefixed server",
			sep:       '.',
			prefix:    "INBOX",
			mailboxes: []string{"INBOX", "Archive"},
			friendly:  "Archive",
			want:      "Archive",
		},
		{
			name:     "no listing falls back to the prefixed guess",
			sep:      '.',
			prefix:   "INBOX",
			friendly: "Archive",
			want:     "INBOX.Archive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewFolderMap(tt.sep, tt.prefix)
			for attr, mailbox := range tt.specialUse {
				m.SetSpecialUse(attr, mailbox)
			}
			if tt.mailboxes != nil {
				m.SetMailboxes(tt.mailboxes)
			}
			if got := m.Resolve(tt.friendly); got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.friendly, got, tt.want)
			}
			got, err := m.ResolveExisting(tt.friendly)
			if err != nil {
				t.Fatalf("ResolveExisting(%q) error = %v", tt.friendly, err)
			}
			if got != tt.want {
				t.Errorf("ResolveExisting(%q) = %q, want %q", tt.friendly, got, tt.want)
			}
		})
	}
}

// TestFolderMapResolveUnadvertisedJunk: a Dovecot-style server that
// keeps its junk folder without \Junk still gets "Spam" moves routed to
// it, under either spelling, instead of to a missing INBOX.Spam.
func TestFolderMapResolveUnadvertisedJunk(t *testing.T) {
	tests := []struct {
		mailboxes []string
		friendly  string
		want      string
	}{
		{[]string{"INBOX", "INBOX.Junk"}, "Spam", "INBOX.Junk"},
		{[]string{"INBOX", "INBOX.Spam"}, "Junk", "INBOX.Spam"},
		{[]string{"INBOX", "INBOX.Junk", "INBOX.Spam"}, "Spam", "INBOX.Spam"},
	}
	for _, tt := range tests {
		m := NewFolderMap('.', "INBOX")
		m.SetMailboxes(tt.mailboxes)
		got, err := m.ResolveExisting(tt.friendly)
		if err != nil || got != tt.want {
			t.Errorf("ResolveExisting(%q) with %v = %q, %v; want %q", tt.friendly, tt.mailboxes, got, err, tt.want)
		}
	}

	m := NewFolderMap('.', "INBOX")
	m.SetMailboxes([]string{"INBOX", "INBOX.Sent"})
	if got, err := m.ResolveExisting("Spam"); err == nil {
		t.Errorf("ResolveExisting(%q) = %q, want an error when no junk folder exists", "Spam", got)
	}
}

// TestFolderMapResolveArchiveFallsBackToAll pins the archive resolution
// order: \Archive, then a listed Archive/Archives folder (prefixed before
// unprefixed), and only then \All, which is Gmail's archive.
func TestFolderMapResolveArchiveFallsBackToAll(t *testing.T) {
	tests := []struct {
		name       string
		sep        byte
		prefix     string
		specialUse map[string]string
		mailboxes  []string
		want       string
	}{
		{
			name:       "gmail-like: only \\All resolves the archive",
			sep:        '/',
			specialUse: map[string]string{SpecialUseAll: "[Gmail]/All Mail"},
			mailboxes:  []string{"INBOX", "[Gmail]/All Mail", "[Gmail]/Trash"},
			want:       "[Gmail]/All Mail",
		},
		{
			name: "\\Archive beats \\All",
			sep:  '/',
			specialUse: map[string]string{
				SpecialUseArchive: "Stash",
				SpecialUseAll:     "[Gmail]/All Mail",
			},
			mailboxes: []string{"INBOX", "Stash", "[Gmail]/All Mail"},
			want:      "Stash",
		},
		{
			name:       "listed prefixed archive beats \\All",
			sep:        '.',
			prefix:     "INBOX",
			specialUse: map[string]string{SpecialUseAll: "INBOX.All"},
			mailboxes:  []string{"INBOX", "INBOX.All", "INBOX.Archive", "Archive"},
			want:       "INBOX.Archive",
		},
		{
			name:       "listed unprefixed archive beats \\All",
			sep:        '.',
			prefix:     "INBOX",
			specialUse: map[string]string{SpecialUseAll: "INBOX.All"},
			mailboxes:  []string{"INBOX", "INBOX.All", "Archives"},
			want:       "Archives",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewFolderMap(tt.sep, tt.prefix)
			for attr, mailbox := range tt.specialUse {
				m.SetSpecialUse(attr, mailbox)
			}
			m.SetMailboxes(tt.mailboxes)
			for _, friendly := range []string{"Archive", "Archives"} {
				got, err := m.ResolveExisting(friendly)
				if err != nil || got != tt.want {
					t.Errorf("ResolveExisting(%q) = %q, %v; want %q", friendly, got, err, tt.want)
				}
			}
		})
	}
}

// TestFolderMapAllOnlyServesArchive: \All is Gmail's archive, not a
// catch-all; other roles and plain names never resolve to it.
func TestFolderMapAllOnlyServesArchive(t *testing.T) {
	m := NewFolderMap('/', "")
	m.SetSpecialUse(SpecialUseAll, "[Gmail]/All Mail")
	m.SetMailboxes([]string{"INBOX", "[Gmail]/All Mail"})
	for _, friendly := range []string{"Spam", "Junk", "Trash", "Clients"} {
		if got, err := m.ResolveExisting(friendly); err == nil {
			t.Errorf("ResolveExisting(%q) = %q, want a does-not-exist error", friendly, got)
		}
	}
}

func TestFolderMapResolveExistingMissingArchive(t *testing.T) {
	m := NewFolderMap('.', "INBOX")
	m.SetMailboxes([]string{"INBOX", "INBOX.Sent", "INBOX.Trash"})

	for _, friendly := range []string{"Archive", "Archives"} {
		got, err := m.ResolveExisting(friendly)
		if err == nil {
			t.Fatalf("ResolveExisting(%q) = %q, want an error for a missing archive", friendly, got)
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("ResolveExisting(%q) error = %v, want it to say the folder does not exist", friendly, err)
		}
	}
}
