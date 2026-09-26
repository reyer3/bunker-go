package mail

import "testing"

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
