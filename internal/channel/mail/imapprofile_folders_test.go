package mail

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

// discoverThrough dials the profile server the way the adapter does and
// runs discoverFolders against it.
func discoverThrough(t *testing.T, s *profileServer, cfg AccountConfig) *FolderMap {
	t.Helper()
	client, err := testDialInsecure(s.Addr)(context.Background(), cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	folders, err := discoverFolders(context.Background(), client, cfg)
	if err != nil {
		t.Fatalf("discoverFolders() error = %v", err)
	}
	return folders
}

// TestDiscoverFoldersProfiles checks that discoverFolders learns the
// real separator and special-use mailboxes from both server styles, and
// that Resolve/ResolveExisting land on mailboxes that exist.
func TestDiscoverFoldersProfiles(t *testing.T) {
	tests := []struct {
		name    string
		profile imapProfile
		// wrongSep is a deliberately mistaken configured separator, to
		// prove the server's LIST delimiter wins.
		wrongSep byte
		wantSep  byte
		want     map[string]string // friendly -> mailbox; "" = must not exist
		wantAll  string
	}{
		{
			name:     "gmail-like",
			profile:  gmailProfile(),
			wrongSep: '.',
			wantSep:  '/',
			want: map[string]string{
				"INBOX":  "INBOX",
				"Sent":   "[Gmail]/Sent Mail",
				"Trash":  "[Gmail]/Trash",
				"Spam":   "[Gmail]/Spam",
				"Junk":   "[Gmail]/Spam",
				"Drafts": "[Gmail]/Drafts",
				// Gmail has no \Archive mailbox (archiving there means
				// leaving INBOX; the mail stays in \All), and bunker-go
				// never guesses or creates one, so this must fail.
				"Archive":  "",
				"Archives": "",
			},
			wantAll: "[Gmail]/All Mail",
		},
		{
			name:     "dovecot-like without \\Archive",
			profile:  dovecotProfile(dovecotOptions{}),
			wrongSep: '/',
			wantSep:  '.',
			want: map[string]string{
				"INBOX":              "INBOX",
				"Sent":               "INBOX.Sent",
				"Trash":              "INBOX.Trash",
				"Spam":               "INBOX.Junk",
				"Junk":               "INBOX.Junk",
				"Drafts":             "INBOX.Drafts",
				"Archive":            "INBOX.Archive",
				"Archives":           "INBOX.Archive",
				"Clients":            "INBOX.Clients",
				"Clients.Acme":       "INBOX.Clients.Acme",
				"INBOX.Clients.Acme": "INBOX.Clients.Acme",
			},
		},
		{
			name:     "dovecot-like with \\Archive",
			profile:  dovecotProfile(dovecotOptions{ArchiveAttr: true}),
			wrongSep: '/',
			wantSep:  '.',
			want: map[string]string{
				"Sent":     "INBOX.Sent",
				"Trash":    "INBOX.Trash",
				"Spam":     "INBOX.Junk",
				"Archive":  "INBOX.Archive",
				"Archives": "INBOX.Archive",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newProfileIMAPServer(t, tt.profile)
			cfg := s.Config("cl")
			cfg.FolderSeparator = tt.wrongSep
			folders := discoverThrough(t, s, cfg)

			if folders.separator != tt.wantSep {
				t.Errorf("separator = %q, want %q", folders.separator, tt.wantSep)
			}
			if got := folders.specialUse[SpecialUseAll]; got != tt.wantAll {
				t.Errorf("\\All = %q, want %q", got, tt.wantAll)
			}
			for friendly, want := range tt.want {
				got, err := folders.ResolveExisting(friendly)
				if want == "" {
					if err == nil || !strings.Contains(err.Error(), "does not exist") {
						t.Errorf("ResolveExisting(%q) = %q, %v; want a does-not-exist error", friendly, got, err)
					}
					continue
				}
				if err != nil {
					t.Errorf("ResolveExisting(%q) error = %v", friendly, err)
				}
				if got != want {
					t.Errorf("ResolveExisting(%q) = %q, want %q", friendly, got, want)
				}
				if r := folders.Resolve(friendly); r != want {
					t.Errorf("Resolve(%q) = %q, want %q", friendly, r, want)
				}
			}
		})
	}
}

// TestDiscoverFoldersSkipsGmailNoselectParent: "[Gmail]" is listed but
// cannot hold mail, so it must not pass as a move target.
func TestDiscoverFoldersSkipsGmailNoselectParent(t *testing.T) {
	s := newProfileIMAPServer(t, gmailProfile())
	folders := discoverThrough(t, s, s.Config("cl"))
	if got, err := folders.ResolveExisting("[Gmail]"); err == nil {
		t.Errorf("ResolveExisting(%q) = %q, want an error for a \\Noselect mailbox", "[Gmail]", got)
	}
}

// TestProfileSeedNestedDovecotFolder: a message in a nested user folder
// is stored and fetched under the full '.'-separated name, and the
// adapter's FolderMap resolves that name to itself.
func TestProfileSeedNestedDovecotFolder(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	seeded := s.Seed(t, "INBOX.Clients.Acme", seedMessage{
		From:      "Cliente <client@example.com>",
		Subject:   "Cotización",
		Body:      "cuerpo del mensaje",
		MessageID: "<acme-1@example.com>",
	})
	if seeded.UID == 0 || seeded.UIDValidity == 0 {
		t.Fatalf("Seed() = %+v, want a UID and UIDVALIDITY", seeded)
	}

	got := s.FetchMessage(t, "INBOX.Clients.Acme", "<acme-1@example.com>")
	if got.UID != seeded.UID || got.UIDValidity != seeded.UIDValidity {
		t.Errorf("FetchMessage() at %d.%d, want %d.%d", got.UIDValidity, got.UID, seeded.UIDValidity, seeded.UID)
	}
	if got.Envelope.Subject != "Cotización" {
		t.Errorf("subject = %q, want %q", got.Envelope.Subject, "Cotización")
	}
	if !strings.Contains(got.Raw, "cuerpo del mensaje") {
		t.Errorf("raw message lacks the body: %q", got.Raw)
	}
	if ids := s.MessageIDs(t, "INBOX.Clients"); len(ids) != 0 {
		t.Errorf("parent INBOX.Clients holds %v, want nothing", ids)
	}

	folders := discoverThrough(t, s, s.Config("cl"))
	if mailbox, err := folders.ResolveExisting("INBOX.Clients.Acme"); err != nil || mailbox != "INBOX.Clients.Acme" {
		t.Errorf("ResolveExisting(INBOX.Clients.Acme) = %q, %v", mailbox, err)
	}
}

// TestProfileMoveAsWebmail: a move from another client relocates the
// message (it is not deleted) and reports both addresses.
func TestProfileMoveAsWebmail(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profile  imapProfile
		from, to string
	}{
		{"gmail-like", gmailProfile(), "INBOX", "[Gmail]/Trash"},
		{"dovecot-like", dovecotProfile(dovecotOptions{}), "INBOX", "INBOX.Clients.Acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProfileIMAPServer(t, tc.profile)
			seeded := s.Seed(t, tc.from, seedMessage{MessageID: "<m1@example.com>"})
			s.Seed(t, tc.from, seedMessage{MessageID: "<m2@example.com>"})

			moved := s.MoveAsWebmail(t, tc.from, tc.to, "<m1@example.com>")
			if moved.SourceUID != seeded.UID || moved.SourceUIDValidity != seeded.UIDValidity {
				t.Errorf("source = %d.%d, want %d.%d", moved.SourceUIDValidity, moved.SourceUID, seeded.UIDValidity, seeded.UID)
			}
			if moved.DestUID == 0 || moved.DestUIDValidity == 0 || moved.DestUIDValidity == moved.SourceUIDValidity {
				t.Errorf("destination = %d.%d, want a UID in the destination's own UIDVALIDITY", moved.DestUIDValidity, moved.DestUID)
			}
			if got := s.MessageIDs(t, tc.from); !reflect.DeepEqual(got, []string{"m2@example.com"}) {
				t.Errorf("%s holds %v after the move, want only m2", tc.from, got)
			}
			if got := s.MessageIDs(t, tc.to); !reflect.DeepEqual(got, []string{"m1@example.com"}) {
				t.Errorf("%s holds %v after the move, want m1", tc.to, got)
			}
			if got := s.FetchMessage(t, tc.to, "<m1@example.com>"); got.UID != moved.DestUID {
				t.Errorf("fetched UID %d in %s, want %d", got.UID, tc.to, moved.DestUID)
			}
		})
	}
}

// TestAdapterOrganizeMoveProfiles runs Organize moves through the
// adapter against both server styles, resolving friendly names through
// the discovered special-use attributes and separator.
func TestAdapterOrganizeMoveProfiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile imapProfile
		moveTo  string
		want    string
	}{
		{"gmail-like trash", gmailProfile(), "Trash", "[Gmail]/Trash"},
		{"gmail-like spam", gmailProfile(), "Spam", "[Gmail]/Spam"},
		{"dovecot-like archive without attr", dovecotProfile(dovecotOptions{}), "Archives", "INBOX.Archive"},
		{"dovecot-like archive with attr", dovecotProfile(dovecotOptions{ArchiveAttr: true}), "Archive", "INBOX.Archive"},
		{"dovecot-like spam", dovecotProfile(dovecotOptions{}), "Spam", "INBOX.Junk"},
		{"dovecot-like nested", dovecotProfile(dovecotOptions{}), "Clients.Acme", "INBOX.Clients.Acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProfileIMAPServer(t, tc.profile)
			s.Seed(t, "INBOX", seedMessage{MessageID: "<a@example.com>"})
			cfg := s.Config("cl")
			id := syncOneAndGetID(t, s.Addr, cfg)

			adapter := newAdapter(cfg, nil, nil, testDialInsecure(s.Addr))
			move, err := adapter.OrganizeMove(context.Background(), id, core.OrganizeOp{MoveTo: tc.moveTo})
			if err != nil {
				t.Fatalf("OrganizeMove(%q) error = %v", tc.moveTo, err)
			}
			if move.Folder != tc.want {
				t.Errorf("OrganizeMove(%q).Folder = %q, want %q", tc.moveTo, move.Folder, tc.want)
			}
			if got := s.MessageIDs(t, tc.want); !reflect.DeepEqual(got, []string{"a@example.com"}) {
				t.Errorf("%s holds %v, want the moved message", tc.want, got)
			}
			if got := s.MessageIDs(t, "INBOX"); len(got) != 0 {
				t.Errorf("INBOX still holds %v after the move", got)
			}
		})
	}
}

// TestAdapterOrganizeMoveGmailArchiveFailsLoudly: with no \Archive on a
// Gmail-like server, an archive move must fail and leave the mail put.
func TestAdapterOrganizeMoveGmailArchiveFailsLoudly(t *testing.T) {
	s := newProfileIMAPServer(t, gmailProfile())
	s.Seed(t, "INBOX", seedMessage{MessageID: "<a@example.com>"})
	cfg := s.Config("cl")
	id := syncOneAndGetID(t, s.Addr, cfg)

	adapter := newAdapter(cfg, nil, nil, testDialInsecure(s.Addr))
	if err := adapter.Organize(context.Background(), id, core.OrganizeOp{MoveTo: "Archive"}); err == nil {
		t.Fatal("Organize(MoveTo Archive) error = nil on a server without an archive")
	}
	if got := s.MessageIDs(t, "INBOX"); len(got) != 1 {
		t.Errorf("INBOX holds %v, want the message untouched", got)
	}
}

// TestProfileListSpecialUseOnly: RETURN/SELECT (SPECIAL-USE) filtering
// works like on a real server, so later code may rely on it.
func TestProfileListSpecialUseOnly(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{ArchiveAttr: true}))
	client := s.login(t)
	defer client.Close()
	entries, err := client.List("", "*", &imap.ListOptions{SelectSpecialUse: true}).Collect()
	if err != nil {
		t.Fatalf("LIST (SPECIAL-USE): %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Mailbox)
		if e.Delim != '.' {
			t.Errorf("%s delimiter = %q, want '.'", e.Mailbox, e.Delim)
		}
	}
	want := []string{"INBOX.Archive", "INBOX.Sent", "INBOX.Trash"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("special-use mailboxes = %v, want %v", names, want)
	}

	children, err := client.List("INBOX.Clients.", "%", nil).Collect()
	if err != nil {
		t.Fatalf("LIST INBOX.Clients. %%: %v", err)
	}
	if len(children) != 1 || children[0].Mailbox != "INBOX.Clients.Acme" {
		t.Errorf("children of INBOX.Clients = %+v, want only INBOX.Clients.Acme", children)
	}
}
