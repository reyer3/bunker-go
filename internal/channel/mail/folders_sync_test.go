package mail

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
)

// planMailboxes runs planSyncFolders against a profile server the way
// Run does and returns the chosen mailboxes in order.
func planMailboxes(t *testing.T, s *profileServer, cfg AccountConfig) []string {
	t.Helper()
	folders := discoverThrough(t, s, cfg)
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(s.Addr))
	var out []string
	for _, f := range adapter.planSyncFolders(folders) {
		out = append(out, f.Mailbox)
	}
	return out
}

// TestPlanSyncFolders pins which folders each server style syncs (#52):
// every Dovecot folder but Trash/Junk/Drafts, Gmail's All Mail instead
// of its label views, and sync_folders/exclude_folders on top.
func TestPlanSyncFolders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile imapProfile
		sync    []string
		exclude []string
		want    []string
	}{
		{
			name:    "dovecot default",
			profile: dovecotProfile(dovecotOptions{}),
			// Junk and Drafts have no attribute here: their names exclude them.
			want: []string{"INBOX.Sent", "INBOX.Archive", "INBOX.Clients", "INBOX.Clients.Acme"},
		},
		{
			name:    "dovecot with \\Archive",
			profile: dovecotProfile(dovecotOptions{ArchiveAttr: true}),
			want:    []string{"INBOX.Sent", "INBOX.Archive", "INBOX.Clients", "INBOX.Clients.Acme"},
		},
		{
			name:    "dovecot exclude_folders drops a folder and its subfolders",
			profile: dovecotProfile(dovecotOptions{}),
			exclude: []string{"Clients"},
			want:    []string{"INBOX.Sent", "INBOX.Archive"},
		},
		{
			name:    "dovecot sync_folders replaces the default",
			profile: dovecotProfile(dovecotOptions{}),
			sync:    []string{"Clients.Acme", "Archives"},
			want:    []string{"INBOX.Sent", "INBOX.Clients.Acme", "INBOX.Archive"},
		},
		{
			name:    "dovecot sync_folders minus exclude_folders",
			profile: dovecotProfile(dovecotOptions{}),
			sync:    []string{"Archive", "Clients.Acme"},
			exclude: []string{"INBOX.Clients.Acme"},
			want:    []string{"INBOX.Sent", "INBOX.Archive"},
		},
		{
			name:    "dovecot exclude_folders never excludes INBOX (and all under it)",
			profile: dovecotProfile(dovecotOptions{}),
			exclude: []string{"INBOX"},
			want:    []string{"INBOX.Sent", "INBOX.Archive", "INBOX.Clients", "INBOX.Clients.Acme"},
		},
		{
			name:    "gmail syncs All Mail, not its label views",
			profile: gmailProfile(),
			want:    []string{"[Gmail]/Sent Mail", "[Gmail]/All Mail"},
		},
		{
			name:    "gmail exclude_folders All Mail",
			profile: gmailProfile(),
			exclude: []string{"[Gmail]/All Mail"},
			want:    []string{"[Gmail]/Sent Mail"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProfileIMAPServer(t, tc.profile)
			cfg := s.Config("cl")
			cfg.SyncFolders = tc.sync
			cfg.ExcludeFolders = tc.exclude
			if got := planMailboxes(t, s, cfg); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("planSyncFolders() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanSyncFoldersSkipsTrashSubfolders: mail under Trash is deleted
// mail too, so a folder nested in it is not synced either.
func TestPlanSyncFoldersSkipsTrashSubfolders(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	s.CreateMailbox(t, "INBOX.Trash.Old")
	s.CreateMailbox(t, "INBOX.Proyectos")
	got := planMailboxes(t, s, s.Config("cl"))
	want := []string{"INBOX.Sent", "INBOX.Archive", "INBOX.Clients", "INBOX.Clients.Acme", "INBOX.Proyectos"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("planSyncFolders() = %v, want %v", got, want)
	}
}

// TestPlanSyncFoldersSkipsDecoySent: a folder literally named "Sent"
// that is not the \Sent mailbox would share the real Sent's ids, so it
// is left out.
func TestPlanSyncFoldersSkipsDecoySent(t *testing.T) {
	s := newProfileIMAPServer(t, imapProfile{
		Name:  "generic",
		Delim: '/',
		Mailboxes: []profileMailbox{
			{Name: "INBOX"},
			{Name: "Sent Items", Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}},
			{Name: "Sent"},
			{Name: "Work"},
		},
	})
	if got, want := planMailboxes(t, s, s.Config("cl")), []string{"Sent Items", "Work"}; !reflect.DeepEqual(got, want) {
		t.Errorf("planSyncFolders() = %v, want %v", got, want)
	}
}

// TestPlanSyncFoldersMissingConfiguredFolderIsLogged: a sync_folders
// entry the server lacks is reported at error level (fail loudly), and
// the rest still sync.
func TestPlanSyncFoldersMissingConfiguredFolderIsLogged(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	cfg := s.Config("cl")
	cfg.SyncFolders = []string{"Nope", "Archive"}
	folders := discoverThrough(t, s, cfg)

	var logs bytes.Buffer
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(s.Addr))
	adapter.logger = slog.New(slog.NewTextHandler(&logs, nil))

	var got []string
	for _, f := range adapter.planSyncFolders(folders) {
		got = append(got, f.Mailbox)
	}
	if want := []string{"INBOX.Sent", "INBOX.Archive"}; !reflect.DeepEqual(got, want) {
		t.Errorf("planSyncFolders() = %v, want %v", got, want)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "Nope") {
		t.Errorf("log = %q, want an error naming the missing folder", logs.String())
	}
}

// runAdapter starts adapter.Run against sink and stops it at cleanup.
func runAdapter(t *testing.T, adapter *Adapter, sink core.Sink) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// waitUntil polls cond until it holds or timeout passes.
func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// storedByMessageID returns every stored item with messageID (as the
// envelope reports it, without angle brackets).
func (s *fakeSink) storedByMessageID(messageID string) []core.Item {
	s.lock()
	defer s.unlock()
	var out []core.Item
	for _, item := range s.items {
		if item.Meta["message_id"] == strings.Trim(messageID, "<>") {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// waitStoredIn waits until the message is stored exactly once, in
// folder, and returns that item.
func waitStoredIn(t *testing.T, sink *fakeSink, messageID, folder string) core.Item {
	t.Helper()
	var item core.Item
	waitUntil(t, 5*time.Second, messageID+" stored in "+folder, func() bool {
		items := sink.storedByMessageID(messageID)
		if len(items) != 1 || items[0].Meta["folder"] != folder {
			return false
		}
		item = items[0]
		return true
	})
	return item
}

// fastPollAdapter is an adapter for s whose folder poll runs every few
// milliseconds, so tests see a poll without waiting minutes.
func fastPollAdapter(s *profileServer, cfg AccountConfig) *Adapter {
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(s.Addr))
	adapter.folderPollInterval = 50 * time.Millisecond
	return adapter
}

// TestAdapterRunSyncsDovecotFolders covers #52 on a generic server: mail
// in an archive and a nested user folder shows up, under ids naming its
// mailbox and with the folder in Meta, and Trash/Junk stay out.
func TestAdapterRunSyncsDovecotFolders(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	s.Seed(t, "INBOX", seedMessage{MessageID: "<inbox@example.com>", Subject: "Entrada"})
	archived := s.Seed(t, "INBOX.Archive", seedMessage{MessageID: "<archived@example.com>", Subject: "Archivado"})
	s.Seed(t, "INBOX.Clients.Acme", seedMessage{MessageID: "<acme@example.com>", Subject: "Cotización"})
	s.Seed(t, "INBOX.Trash", seedMessage{MessageID: "<trash@example.com>"})
	s.Seed(t, "INBOX.Junk", seedMessage{MessageID: "<junk@example.com>"})
	s.Seed(t, "INBOX.Drafts", seedMessage{MessageID: "<draft@example.com>"})

	sink := newFakeSink()
	runAdapter(t, fastPollAdapter(s, s.Config("cl")), sink)

	waitStoredIn(t, sink, "<inbox@example.com>", "INBOX")
	item := waitStoredIn(t, sink, "<archived@example.com>", "INBOX.Archive")
	waitStoredIn(t, sink, "<acme@example.com>", "INBOX.Clients.Acme")

	if want := itemID("cl", "INBOX.Archive", archived.UIDValidity, archived.UID); item.ID != want {
		t.Errorf("archived id = %q, want %q", item.ID, want)
	}
	if item.Subject != "Archivado" {
		t.Errorf("archived subject = %q", item.Subject)
	}

	// Give a few more polls the chance to (wrongly) pick them up.
	time.Sleep(300 * time.Millisecond)
	for _, id := range []string{"<trash@example.com>", "<junk@example.com>", "<draft@example.com>"} {
		if got := sink.storedByMessageID(id); len(got) != 0 {
			t.Errorf("%s stored as %v, want Trash/Junk/Drafts never synced", id, got[0].ID)
		}
	}
}

// TestAdapterRunHonorsExcludeAndSyncFolders runs the sync_folders and
// exclude_folders options end to end.
func TestAdapterRunHonorsExcludeAndSyncFolders(t *testing.T) {
	for _, tc := range []struct {
		name          string
		sync, exclude []string
		want, notWant string
	}{
		{name: "exclude_folders", exclude: []string{"Clients"}, want: "INBOX.Archive", notWant: "INBOX.Clients.Acme"},
		{name: "sync_folders", sync: []string{"Clients.Acme"}, want: "INBOX.Clients.Acme", notWant: "INBOX.Archive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
			s.Seed(t, "INBOX.Archive", seedMessage{MessageID: "<INBOX.Archive@example.com>"})
			s.Seed(t, "INBOX.Clients.Acme", seedMessage{MessageID: "<INBOX.Clients.Acme@example.com>"})
			cfg := s.Config("cl")
			cfg.SyncFolders = tc.sync
			cfg.ExcludeFolders = tc.exclude

			sink := newFakeSink()
			runAdapter(t, fastPollAdapter(s, cfg), sink)

			waitStoredIn(t, sink, "<"+tc.want+"@example.com>", tc.want)
			time.Sleep(300 * time.Millisecond)
			if got := sink.storedByMessageID("<" + tc.notWant + "@example.com>"); len(got) != 0 {
				t.Errorf("%s synced as %s, want it left out", tc.notWant, got[0].ID)
			}
		})
	}
}

// TestAdapterRunSyncsNewMailInOtherFolderOnPoll: a message filed into a
// subfolder after startup (a server-side filter, another client) shows
// up on the next poll, not only on reconnect.
func TestAdapterRunSyncsNewMailInOtherFolderOnPoll(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	s.Seed(t, "INBOX", seedMessage{MessageID: "<first@example.com>"})
	sink := newFakeSink()
	runAdapter(t, fastPollAdapter(s, s.Config("cl")), sink)
	waitStoredIn(t, sink, "<first@example.com>", "INBOX")

	s.Seed(t, "INBOX.Clients.Acme", seedMessage{MessageID: "<later@example.com>"})
	waitStoredIn(t, sink, "<later@example.com>", "INBOX.Clients.Acme")
}

// TestAdapterRunSyncsGmailAllMail covers #52 on Gmail: All Mail is
// synced, so an archived message (only in All Mail) shows up, and a
// message in both INBOX and All Mail is stored once, as its INBOX copy.
func TestAdapterRunSyncsGmailAllMail(t *testing.T) {
	s := newProfileIMAPServer(t, gmailProfile())
	archived := s.Seed(t, "[Gmail]/All Mail", seedMessage{MessageID: "<archived@example.com>", Subject: "Archivado"})
	// The fake does not mirror INBOX into All Mail, so do it by hand, the
	// way real Gmail shows every INBOX message there too.
	s.Seed(t, "INBOX", seedMessage{MessageID: "<both@example.com>", Subject: "En ambos"})
	s.Seed(t, "[Gmail]/All Mail", seedMessage{MessageID: "<both@example.com>", Subject: "En ambos"})
	s.Seed(t, "[Gmail]/All Mail", seedMessage{MessageID: "<draft@example.com>", Flags: []imap.Flag{imap.FlagDraft}})
	s.Seed(t, "[Gmail]/Trash", seedMessage{MessageID: "<trash@example.com>"})

	sink := newFakeSink()
	runAdapter(t, fastPollAdapter(s, s.Config("cl")), sink)

	item := waitStoredIn(t, sink, "<archived@example.com>", "[Gmail]/All Mail")
	if want := itemID("cl", "[Gmail]/All Mail", archived.UIDValidity, archived.UID); item.ID != want {
		t.Errorf("archived id = %q, want %q", item.ID, want)
	}
	waitStoredIn(t, sink, "<both@example.com>", "INBOX")

	time.Sleep(300 * time.Millisecond)
	if got := sink.storedByMessageID("<both@example.com>"); len(got) != 1 || got[0].Meta["folder"] != "INBOX" {
		t.Errorf("message in INBOX and All Mail stored as %v, want once, from INBOX", ids(got))
	}
	for _, id := range []string{"<draft@example.com>", "<trash@example.com>"} {
		if got := sink.storedByMessageID(id); len(got) != 0 {
			t.Errorf("%s stored as %v, want it left out", id, ids(got))
		}
	}
}

// TestAdapterRunGmailArchiveKeepsItem: archiving on Gmail drops the
// message from INBOX while it stays in All Mail; the item must follow it
// there (#53), not vanish.
func TestAdapterRunGmailArchiveKeepsItem(t *testing.T) {
	s := newProfileIMAPServer(t, gmailProfile())
	s.Seed(t, "INBOX", seedMessage{MessageID: "<a@example.com>", Subject: "Archívame"})
	allCopy := s.Seed(t, "[Gmail]/All Mail", seedMessage{MessageID: "<a@example.com>", Subject: "Archívame"})

	sink := newFakeSink()
	runAdapter(t, fastPollAdapter(s, s.Config("cl")), sink)
	waitStoredIn(t, sink, "<a@example.com>", "INBOX")

	expungeByMessageID(t, s, "INBOX", "<a@example.com>")

	item := waitStoredIn(t, sink, "<a@example.com>", "[Gmail]/All Mail")
	if want := itemID("cl", "[Gmail]/All Mail", allCopy.UIDValidity, allCopy.UID); item.ID != want {
		t.Errorf("archived id = %q, want %q", item.ID, want)
	}
}

// expungeByMessageID deletes a message for good from another client.
func expungeByMessageID(t *testing.T, s *profileServer, mailbox, messageID string) {
	t.Helper()
	client := s.login(t)
	defer client.Close()
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	uid := findUIDByMessageID(t, client, mailbox, messageID)
	if err := client.Store(imap.UIDSetNum(uid), &imap.StoreFlags{
		Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted},
	}, nil).Close(); err != nil {
		t.Fatalf("store \\Deleted: %v", err)
	}
	if _, err := client.Expunge().Collect(); err != nil {
		t.Fatalf("expunge: %v", err)
	}
}

func ids(items []core.Item) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

// TestAdapterRunFollowsWebmailMove covers #53: a message moved from
// INBOX to the archive in webmail keeps its item, relocated to the
// archive with the same content and read state, while a real delete
// still removes its item.
func TestAdapterRunFollowsWebmailMove(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	s.Seed(t, "INBOX", seedMessage{MessageID: "<move@example.com>", Subject: "Muévelo", Body: "hola", Flags: []imap.Flag{imap.FlagSeen}})
	s.Seed(t, "INBOX", seedMessage{MessageID: "<gone@example.com>", Subject: "Bórralo"})

	sink := newFakeSink()
	runAdapter(t, fastPollAdapter(s, s.Config("cl")), sink)
	before := waitStoredIn(t, sink, "<move@example.com>", "INBOX")
	waitStoredIn(t, sink, "<gone@example.com>", "INBOX")

	// A full read saved the body (core.Service does this on read); the
	// relocated item must keep it.
	before.Body = "cuerpo completo leído"
	if err := sink.Upsert(context.Background(), before); err != nil {
		t.Fatal(err)
	}

	moved := s.MoveAsWebmail(t, "INBOX", "INBOX.Archive", "<move@example.com>")
	after := waitStoredIn(t, sink, "<move@example.com>", "INBOX.Archive")
	if want := itemID("cl", "INBOX.Archive", moved.DestUIDValidity, moved.DestUID); after.ID != want {
		t.Errorf("moved id = %q, want %q", after.ID, want)
	}
	if after.Subject != before.Subject || after.Thread != before.Thread || after.Body != before.Body {
		t.Errorf("moved item = %+v, want the content of %+v", after, before)
	}
	if after.Unread {
		t.Error("moved item is unread, want the read state kept")
	}
	if _, err := sink.getItem(before.ID); err == nil {
		t.Errorf("old id %s still stored after the move", before.ID)
	}

	expungeByMessageID(t, s, "INBOX", "<gone@example.com>")
	waitUntil(t, 5*time.Second, "deleted message dropped", func() bool {
		return len(sink.storedByMessageID("<gone@example.com>")) == 0
	})

	// A later move between two polled folders is found by the poll.
	moved = s.MoveAsWebmail(t, "INBOX.Archive", "INBOX.Clients.Acme", "<move@example.com>")
	after = waitStoredIn(t, sink, "<move@example.com>", "INBOX.Clients.Acme")
	if want := itemID("cl", "INBOX.Clients.Acme", moved.DestUIDValidity, moved.DestUID); after.ID != want {
		t.Errorf("moved id = %q, want %q", after.ID, want)
	}
	if after.Body != before.Body || after.Unread {
		t.Errorf("item after second move = %+v, want body and read state kept", after)
	}

	// And a delete from a polled folder still removes it.
	expungeByMessageID(t, s, "INBOX.Clients.Acme", "<move@example.com>")
	waitUntil(t, 5*time.Second, "deleted archived message dropped", func() bool {
		return len(sink.storedByMessageID("<move@example.com>")) == 0
	})
}

// TestAdapterRunFollowsMoveMadeWhileStopped: a message moved while the
// daemon was down is relocated by the startup reconciliation, not
// dropped and synced again as a new item (which would lose its body).
func TestAdapterRunFollowsMoveMadeWhileStopped(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	s.Seed(t, "INBOX", seedMessage{MessageID: "<m@example.com>"})
	cfg := s.Config("cl")
	sink := newFakeSink()

	first := fastPollAdapter(s, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx, sink) }()
	old := waitStoredIn(t, sink, "<m@example.com>", "INBOX")
	cancel()
	<-done
	old.Body = "cuerpo completo leído"
	if err := sink.Upsert(context.Background(), old); err != nil {
		t.Fatal(err)
	}

	s.MoveAsWebmail(t, "INBOX", "INBOX.Archive", "<m@example.com>")

	runAdapter(t, fastPollAdapter(s, cfg), sink)
	item := waitStoredIn(t, sink, "<m@example.com>", "INBOX.Archive")
	if item.ID == old.ID {
		t.Errorf("item kept its INBOX id %s after the move", old.ID)
	}
	if item.Body != old.Body {
		t.Errorf("body = %q, want the stored %q kept: a move is not a delete", item.Body, old.Body)
	}
}

// TestAdapterNonInboxItemEndToEnd covers #52's id scheme: an item synced
// from a non-INBOX folder can be read, have its attachment downloaded,
// be marked read and be moved, each acting on its own mailbox.
func TestAdapterNonInboxItemEndToEnd(t *testing.T) {
	s := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	appendMessage(t, s.Addr, "INBOX.Archive", multipartMessage)
	// A message at the same UID in INBOX: acting on the wrong mailbox
	// would touch this one instead.
	appendMessage(t, s.Addr, "INBOX", multipartMessage)
	cfg := s.Config("cl")

	sink := newFakeSink()
	adapter := fastPollAdapter(s, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	var id string
	waitUntil(t, 5*time.Second, "archived item", func() bool {
		sink.lock()
		defer sink.unlock()
		for _, item := range sink.items {
			if item.Meta["folder"] == "INBOX.Archive" {
				id = item.ID
				return true
			}
		}
		return false
	})
	cancel()
	<-done

	_, folder, _, _, err := parseItemID(id)
	if err != nil || folder != "INBOX.Archive" {
		t.Fatalf("parseItemID(%q) folder = %q, %v; want INBOX.Archive", id, folder, err)
	}

	item, err := adapter.Fetch(context.Background(), id)
	if err != nil {
		t.Fatalf("Fetch(%s) error = %v", id, err)
	}
	if item.ID != id || item.Meta["folder"] != "INBOX.Archive" || len(item.Attachments) != 1 {
		t.Fatalf("Fetch(%s) = id %s, folder %q, %d attachments", id, item.ID, item.Meta["folder"], len(item.Attachments))
	}
	rc, err := adapter.DownloadAttachment(context.Background(), item, 0)
	if err != nil {
		t.Fatalf("DownloadAttachment() error = %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "fake video bytes" {
		t.Errorf("attachment = %q", data)
	}

	seen := true
	if err := adapter.Organize(context.Background(), id, core.OrganizeOp{Seen: &seen}); err != nil {
		t.Fatalf("Organize(seen) error = %v", err)
	}
	if !hasFlag(s.FetchMessage(t, "INBOX.Archive", "<"+item.Meta["message_id"]+">").Flags, imap.FlagSeen) {
		t.Error("archived message not \\Seen after mark-read")
	}
	if hasFlag(s.FetchMessage(t, "INBOX", "<"+item.Meta["message_id"]+">").Flags, imap.FlagSeen) {
		t.Error("mark-read of the archived item marked the INBOX message instead")
	}

	move, err := adapter.OrganizeMove(context.Background(), id, core.OrganizeOp{MoveTo: "Clients.Acme"})
	if err != nil {
		t.Fatalf("OrganizeMove() error = %v", err)
	}
	if move.Folder != "INBOX.Clients.Acme" {
		t.Errorf("move.Folder = %q, want INBOX.Clients.Acme", move.Folder)
	}
	if _, folder, _, _, err := parseItemID(move.ID); err != nil || folder != "INBOX.Clients.Acme" {
		t.Errorf("move.ID = %q (folder %q, %v), want an id in INBOX.Clients.Acme", move.ID, folder, err)
	}
	if got := s.MessageIDs(t, "INBOX.Archive"); len(got) != 0 {
		t.Errorf("INBOX.Archive still holds %v after the move", got)
	}
	if got := s.MessageIDs(t, "INBOX"); len(got) != 1 {
		t.Errorf("INBOX holds %v, want its own message untouched", got)
	}
	if _, err := adapter.Fetch(context.Background(), move.ID); err != nil {
		t.Errorf("Fetch(moved id %s) error = %v", move.ID, err)
	}
}
