package mail

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testIMAPUsername = "alice"
	testIMAPPassword = "s3cr3t"
)

// memIMAPServer starts an in-process, insecure-auth IMAP server backed by
// imapmemserver, seeded with a user and an INBOX. It never touches a real
// network interface beyond loopback, and the listener is closed by the
// returned io.Closer (via t.Cleanup).
func newMemIMAPServer(t *testing.T) (addr string, mem *imapmemserver.User, server *imapserver.Server) {
	t.Helper()

	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(testIMAPUsername, testIMAPPassword)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	memServer.AddUser(user)

	server = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return pollingIdleSession{memServer.NewSession().(idleTestSession)}, nil, nil
		},
		InsecureAuth: true,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIdle:      {},
			imap.CapMove:      {},
			imap.CapUIDPlus:   {},
		},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go server.Serve(ln)
	t.Cleanup(func() {
		server.Close()
	})

	return ln.Addr().String(), user, server
}

// idleTestSession is what imapmemserver's sessions implement; MOVE must
// stay visible or the server rejects the advertised capability.
type idleTestSession interface {
	imapserver.Session
	imapserver.SessionMove
}

// pollingIdleSession makes the test server's IDLE behave like Dovecot's
// (cmd-idle.c checks for pending changes right after "+ idling"): it
// flushes updates queued before IDLE and keeps polling until stopped.
// Stock imapmemserver only sends an update that arrives DURING IDLE, so
// an EXPUNGE landing between a client's FETCH and its IDLE was held back
// until an unrelated update came along.
type pollingIdleSession struct{ idleTestSession }

func (s pollingIdleSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	for {
		if err := s.idleTestSession.Poll(w, true); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// testDialInsecure is a dialFunc that dials addr in plaintext (no TLS)
// and ignores passwordSource/tokenSource in favor of the fixed test
// credentials, mirroring dialReal's LOGIN branch.
func testDialInsecure(addr string) dialFunc {
	return func(ctx context.Context, cfg AccountConfig, _ PasswordSource, _ TokenSource, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		client := imapclient.New(conn, &imapclient.Options{UnilateralDataHandler: handler})
		if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
			client.Close()
			return nil, err
		}
		return client, nil
	}
}

// appendMessage appends a minimal RFC 5322 message to mailbox via a
// throwaway authenticated connection, so tests can seed or grow a
// mailbox independently of the adapter under test.
func appendMessage(t *testing.T, addr, mailbox, raw string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for append: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for append: %v", err)
	}
	cmd := client.Append(mailbox, int64(len(raw)), nil)
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append wait: %v", err)
	}
}

// appendMessageWithFlags is appendMessage plus an explicit initial flag
// set, so tests can seed a message that already carries Dovecot
// keywords (custom IMAP flags) or system flags.
func appendMessageWithFlags(t *testing.T, addr, mailbox, raw string, flags []imap.Flag) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for append: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for append: %v", err)
	}
	cmd := client.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flags})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		t.Fatalf("append write: %v", err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("append close: %v", err)
	}
	if _, err := cmd.Wait(); err != nil {
		t.Fatalf("append wait: %v", err)
	}
}

// markReadEvent records one fakeSink.MarkRead call, for tests that
// assert IDLE flag-update reconciliation (T9b).
type markReadEvent struct {
	id   string
	read bool
}

// fakeSink is a minimal in-memory core.Sink for adapter tests.
type fakeSink struct {
	mu       chan struct{} // 1-buffered mutex
	items    map[string]core.Item
	cursors  map[string]string
	upserts  chan core.Item
	deletes  chan string
	markRead chan markReadEvent
}

func newFakeSink() *fakeSink {
	s := &fakeSink{
		mu:       make(chan struct{}, 1),
		items:    make(map[string]core.Item),
		cursors:  make(map[string]string),
		upserts:  make(chan core.Item, 64),
		deletes:  make(chan string, 64),
		markRead: make(chan markReadEvent, 64),
	}
	s.mu <- struct{}{}
	return s
}

func (s *fakeSink) lock()   { <-s.mu }
func (s *fakeSink) unlock() { s.mu <- struct{}{} }

func (s *fakeSink) Upsert(_ context.Context, item core.Item) error {
	s.lock()
	s.items[item.ID] = item
	s.unlock()
	select {
	case s.upserts <- item:
	default:
	}
	return nil
}

func (s *fakeSink) MarkRead(_ context.Context, id string, read bool) error {
	s.lock()
	item, ok := s.items[id]
	if !ok {
		s.unlock()
		return core.ErrNotFound
	}
	item.Unread = !read
	s.items[id] = item
	s.unlock()
	select {
	case s.markRead <- markReadEvent{id: id, read: read}:
	default:
	}
	return nil
}

// MarkThreadReadUpTo flips Unread=false on every matching item (same
// channel/account/thread, not FromMe, Timestamp <= upTo), mirroring the
// real store's filter.
func (s *fakeSink) MarkThreadReadUpTo(_ context.Context, channel core.Channel, account, thread string, upTo time.Time) error {
	s.lock()
	for id, item := range s.items {
		if item.Channel != channel || item.Account != account || item.Thread != thread {
			continue
		}
		if item.FromMe || !item.Unread || item.Timestamp.After(upTo) {
			continue
		}
		item.Unread = false
		s.items[id] = item
	}
	s.unlock()
	return nil
}

func (s *fakeSink) Delete(_ context.Context, id string) error {
	s.lock()
	if _, ok := s.items[id]; !ok {
		s.unlock()
		return core.ErrNotFound
	}
	delete(s.items, id)
	s.unlock()
	select {
	case s.deletes <- id:
	default:
	}
	return nil
}

func (s *fakeSink) Cursor(_ context.Context, key string) (string, error) {
	s.lock()
	defer s.unlock()
	return s.cursors[key], nil
}

func (s *fakeSink) SetCursor(_ context.Context, key, val string) error {
	s.lock()
	defer s.unlock()
	s.cursors[key] = val
	return nil
}

func (s *fakeSink) EditItem(_ context.Context, id, body string) error {
	s.lock()
	defer s.unlock()
	item, ok := s.items[id]
	if !ok {
		return core.ErrNotFound
	}
	item.Body = body
	item.Edited = true
	s.items[id] = item
	return nil
}

func (s *fakeSink) RevokeItem(_ context.Context, id string) error {
	s.lock()
	defer s.unlock()
	item, ok := s.items[id]
	if !ok {
		return core.ErrNotFound
	}
	item.Body = ""
	item.Deleted = true
	s.items[id] = item
	return nil
}

func (s *fakeSink) SetReaction(_ context.Context, id string, reaction core.Reaction) error {
	s.lock()
	defer s.unlock()
	item, ok := s.items[id]
	if !ok {
		return core.ErrNotFound
	}
	var kept []core.Reaction
	for _, r := range item.Reactions {
		if r.Sender != reaction.Sender {
			kept = append(kept, r)
		}
	}
	if reaction.Emoji != "" {
		kept = append(kept, reaction)
	}
	item.Reactions = kept
	s.items[id] = item
	return nil
}

// getItem returns the stored item for id, or core.ErrNotFound — used by
// tests that poll for a startup reconciliation (T9c) to finish.
func (s *fakeSink) getItem(id string) (core.Item, error) {
	s.lock()
	defer s.unlock()
	item, ok := s.items[id]
	if !ok {
		return core.Item{}, core.ErrNotFound
	}
	return item, nil
}

// Get implements the read capability mail.Adapter's IDLE FETCH keyword
// reconciliation (T14a) needs from its Sink, mirroring core.Store.Get.
func (s *fakeSink) Get(_ context.Context, id string) (core.Item, error) {
	return s.getItem(id)
}

// List implements the read capability mail.Adapter's startup
// reconciliation (T9c) needs from its Sink, mirroring core.Store.List
// closely enough for this fake (channel/account filters only).
func (s *fakeSink) List(_ context.Context, filter core.Filter) ([]core.Item, error) {
	s.lock()
	defer s.unlock()
	var out []core.Item
	for _, it := range s.items {
		if filter.Channel != "" && it.Channel != filter.Channel {
			continue
		}
		if filter.Account != "" && it.Account != filter.Account {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}

func rawMessage(messageID, references, subject, from, to, body string) string {
	var refHeader string
	if references != "" {
		refHeader = "References: " + references + "\r\n"
	}
	return fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-Id: %s\r\n%sDate: Fri, 25 Sep 2026 10:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, to, subject, messageID, refHeader, body,
	)
}

func waitForUpsert(t *testing.T, sink *fakeSink, timeout time.Duration) core.Item {
	t.Helper()
	select {
	case item := <-sink.upserts:
		return item
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an upsert")
		return core.Item{}
	}
}

func waitForDelete(t *testing.T, sink *fakeSink, timeout time.Duration) string {
	t.Helper()
	select {
	case id := <-sink.deletes:
		return id
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a delete")
		return ""
	}
}

func waitForMarkRead(t *testing.T, sink *fakeSink, timeout time.Duration) markReadEvent {
	t.Helper()
	select {
	case ev := <-sink.markRead:
		return ev
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a MarkRead")
		return markReadEvent{}
	}
}

func TestAdapterRunInitialSync(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "Meet recording",
		"Alice <alice@example.org>", "alice@example.org", "Body one",
	))
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg2@example.org>", "<msg1@example.org>", "Re: Meet recording",
		"Bob <bob@example.org>", "alice@example.org", "Body two",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	first := waitForUpsert(t, sink, 5*time.Second)
	second := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	items := map[string]core.Item{first.ID: first, second.ID: second}
	var found bool
	for _, item := range items {
		if item.ThreadName != "Meet recording" {
			t.Errorf("item %s ThreadName = %q, want %q", item.ID, item.ThreadName, "Meet recording")
		}
		if item.Subject == "Re: Meet recording" {
			found = true
			if item.Thread != "msg1@example.org" {
				t.Errorf("reply Thread = %q, want the root message id", item.Thread)
			}
		}
	}
	if !found {
		t.Error("never saw the reply message upserted")
	}

	if v, err := sink.Cursor(context.Background(), cursorKey("cl", "inbox.last_uid")); err != nil || v == "" {
		t.Errorf("last_uid cursor = %q, err = %v, want a persisted UID", v, err)
	}
}

// TestAdapterRunSetsFromMeForOwnAddress proves buildItem sets FromMe
// when an INBOX message's From matches the account's own address
// (cfg.Username, compared case-insensitively) — which happens for a
// message the user sent to themselves, or one a mail client filed back into
// INBOX after sending. It must never be derived from \Seen/Unread.
func TestAdapterRunSetsFromMeForOwnAddress(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<from-other@example.org>", "", "From Bob",
		"Bob <bob@example.org>", "alice@example.org", "hi",
	))
	appendMessage(t, addr, "INBOX", rawMessage(
		"<from-self@example.org>", "", "Note to self",
		"Alice <ALICE@Example.ORG>", "alice@example.org", "reminder",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '.', Username: "alice@example.org"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	first := waitForUpsert(t, sink, 5*time.Second)
	second := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	for _, item := range []core.Item{first, second} {
		switch item.Subject {
		case "From Bob":
			if item.FromMe {
				t.Errorf("item %q FromMe = true, want false (From bob@example.org)", item.Subject)
			}
		case "Note to self":
			if !item.FromMe {
				t.Errorf("item %q FromMe = false, want true (From matches the account's own address, case-insensitively)", item.Subject)
			}
		default:
			t.Errorf("unexpected item subject %q", item.Subject)
		}
	}
}

func TestAdapterRunPicksUpNewMailWhileIdle(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "First", "a@example.org", "r@example.org", "one",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	waitForUpsert(t, sink, 5*time.Second) // initial sync

	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg2@example.org>", "", "Arrived during idle", "b@example.org", "r@example.org", "two",
	))

	item := waitForUpsert(t, sink, 5*time.Second)
	if item.Subject != "Arrived during idle" {
		t.Errorf("Subject = %q, want %q", item.Subject, "Arrived during idle")
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

// TestAdapterRunSyncsDovecotKeywordsAsLabels covers T14(a): a Dovecot
// custom IMAP keyword on FLAGS becomes a Labels entry on initial sync,
// while system flags (leading \, e.g. \Seen) and RFC 5788 server-defined
// keywords (leading $, e.g. $Forwarded/$MDNSent) are excluded — neither
// describes a user label, and the plain keyword→Labels mapping already
// existed before this task; only the $-prefix exclusion is new here.
func TestAdapterRunSyncsDovecotKeywordsAsLabels(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessageWithFlags(t, addr, "INBOX", rawMessage(
		"<a@x>", "", "Labeled", "a@x", "r@x", "b",
	), []imap.Flag{imap.FlagSeen, "bunker-test", "$Forwarded"})

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	item := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}

	if len(item.Labels) != 1 || item.Labels[0] != "bunker-test" {
		t.Errorf("Labels = %v, want exactly [\"bunker-test\"] (\\Seen and $Forwarded must be excluded)", item.Labels)
	}
}

func TestAdapterRunReconnectsAfterConnectionLoss(t *testing.T) {
	addr, _, server := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "First", "a@example.org", "r@example.org", "one",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	adapter.backoff = func(int) time.Duration { return time.Millisecond } // fast retries

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	waitForUpsert(t, sink, 5*time.Second) // initial sync completed over the first connection

	// Sever every connection the server holds: the adapter's IDLE loop
	// must notice (via client.Closed()) and Run must keep retrying to
	// reconnect (to the now-closed server) instead of returning early.
	server.Close()

	select {
	case err := <-done:
		t.Fatalf("Run() returned early with err = %v, want it to keep retrying until ctx is canceled", err)
	case <-time.After(50 * time.Millisecond):
		// still retrying, as expected
	}

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after ctx was canceled")
	}
}

// TestSyncFromUsesCallerMessageCountNotClientCache covers the startup
// stall: go-imap v2 releases Select().Wait() before it stores the
// mailbox in client.Mailbox(), so syncFrom used to read a nil cache,
// conclude "no messages", skip the initial sync and IDLE forever (about
// 1 run in 15 under CPU load). That window cannot be forced through the
// public client, so this pins the contract instead: the count the
// caller passes (from the SELECT result or an EXISTS update) is the only
// source. The cache here says 1 message; a caller count of 0 must fetch
// nothing and a count of 1 must fetch the message, which fails if
// syncFrom ever reads client.Mailbox() again.
func TestSyncFromUsesCallerMessageCountNotClientCache(t *testing.T) {
	for _, tc := range []struct {
		name        string
		numMessages uint32
		wantUpserts int
	}{
		{"caller count 0 wins over cached 1", 0, 0},
		{"caller count 1 fetches", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, _, _ := newMemIMAPServer(t)
			appendMessage(t, addr, "INBOX", rawMessage("<a@x>", "", "S", "a@x", "r@x", "b"))
			cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
			adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
			ctx := context.Background()

			client, err := testDialInsecure(addr)(ctx, cfg, nil, nil, nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer client.Close()
			mbox, err := client.Select("INBOX", nil).Wait()
			if err != nil {
				t.Fatalf("select: %v", err)
			}
			folders, err := discoverFolders(ctx, client, cfg)
			if err != nil {
				t.Fatalf("discover folders: %v", err)
			}
			if cached := client.Mailbox(); cached == nil || cached.NumMessages != 1 {
				t.Fatalf("precondition: client cache = %+v, want NumMessages 1", cached)
			}

			sink := newFakeSink()
			if _, err := adapter.syncFrom(ctx, client, sink, folders, mbox.UIDValidity, 0, newSeqTracker(), tc.numMessages, "INBOX"); err != nil {
				t.Fatalf("syncFrom() error = %v", err)
			}
			if got := len(sink.upserts); got != tc.wantUpserts {
				t.Errorf("upserts = %d, want %d", got, tc.wantUpserts)
			}
		})
	}
}

// markSeenElsewhere flips \Seen on uid in mailbox via a second,
// independent connection, simulating a read made on another client
// (Roundcube, the phone's Gmail app) while the adapter under test holds
// its own connection open.
func markSeenElsewhere(t *testing.T, addr, mailbox string, uid imap.UID) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial to mark seen elsewhere: %v", err)
	}
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login to mark seen elsewhere: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select to mark seen elsewhere: %v", err)
	}
	storeFlags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
	if err := client.Store(imap.UIDSetNum(uid), storeFlags, nil).Close(); err != nil {
		t.Fatalf("store \\Seen elsewhere: %v", err)
	}
}

// TestReconcileSeenFlagsMarksStoredUnreadItemsNowSeenElsewhere proves the
// core R4 mechanism directly, called with no IDLE command ever issued on
// the adapter's own connection at all — the literal "pre-IDLE window"
// gap the periodic safety-net closes: a \Seen change made from a second
// client before this session's IDLE (or, here, before it ever starts)
// has no unsolicited FETCH to be observed through, so only an explicit
// UID FETCH FLAGS like this one can catch it up.
func TestReconcileSeenFlagsMarksStoredUnreadItemsNowSeenElsewhere(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "Reconcile me",
		"Alice <alice@example.org>", "alice@example.org", "Body one",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	ctx := context.Background()

	client, err := testDialInsecure(addr)(ctx, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatalf("select: %v", err)
	}

	searchData, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		t.Fatalf("uid search: %v", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) != 1 {
		t.Fatalf("uid search returned %d uids, want 1", len(uids))
	}
	uid := uids[0]

	sink := newFakeSink()
	id := itemID("cl", "INBOX", mbox.UIDValidity, uid)
	seed := core.Item{
		ID: id, Channel: core.ChannelMail, Account: "cl",
		Unread: true, Timestamp: time.Now(),
		Meta: map[string]string{"folder": "INBOX"},
	}
	if err := sink.Upsert(ctx, seed); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	// The change happens entirely before reconcileSeenFlags is ever
	// called, on a connection that has never IDLEd — nothing here relies
	// on IDLE at all.
	markSeenElsewhere(t, addr, "INBOX", uid)

	if err := adapter.reconcileSeenFlags(ctx, client, sink, mbox.UIDValidity); err != nil {
		t.Fatalf("reconcileSeenFlags: %v", err)
	}

	got, err := sink.getItem(id)
	if err != nil {
		t.Fatalf("getItem: %v", err)
	}
	if got.Unread {
		t.Errorf("item %s still Unread after reconcileSeenFlags, want false", id)
	}
}

// TestReconcileSeenFlagsIgnoresItemsAlreadyRead proves reconcileSeenFlags
// leaves an already-read item alone (no spurious MarkRead call) and
// skips items outside its (channel, account, INBOX, current uidvalidity)
// scope.
func TestReconcileSeenFlagsIgnoresItemsAlreadyRead(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "Already read",
		"Alice <alice@example.org>", "alice@example.org", "Body",
	))

	cfg := AccountConfig{Name: "cl", IMAPHost: "unused"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))
	ctx := context.Background()

	client, err := testDialInsecure(addr)(ctx, cfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	mbox, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatalf("select: %v", err)
	}

	searchData, err := client.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		t.Fatalf("uid search: %v", err)
	}
	uids := searchData.AllUIDs()
	if len(uids) != 1 {
		t.Fatalf("uid search returned %d uids, want 1", len(uids))
	}
	uid := uids[0]

	sink := newFakeSink()
	// Not unread: reconcileSeenFlags must never touch it (and must never
	// call sink.MarkRead for it).
	alreadyRead := core.Item{
		ID: itemID("cl", "INBOX", mbox.UIDValidity, uid), Channel: core.ChannelMail, Account: "cl",
		Unread: false, Timestamp: time.Now(), Meta: map[string]string{"folder": "INBOX"},
	}
	// Unread, but from a stale mailbox generation (a UIDVALIDITY that
	// does not match this connection's current one) reusing the same
	// raw UID number: reconcileSeenFlags must never mark this one read
	// either, even though the real message at that UID is \Seen —
	// mixing UID numbers across UIDVALIDITY generations would apply an
	// unrelated message's flags to the wrong stored item.
	staleGeneration := core.Item{
		ID: itemID("cl", "INBOX", mbox.UIDValidity+999, uid), Channel: core.ChannelMail, Account: "cl",
		Unread: true, Timestamp: time.Now(), Meta: map[string]string{"folder": "INBOX"},
	}
	if err := sink.Upsert(ctx, alreadyRead); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
	if err := sink.Upsert(ctx, staleGeneration); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	if err := adapter.reconcileSeenFlags(ctx, client, sink, mbox.UIDValidity); err != nil {
		t.Fatalf("reconcileSeenFlags: %v", err)
	}

	got, err := sink.getItem(staleGeneration.ID)
	if err != nil {
		t.Fatalf("getItem: %v", err)
	}
	if !got.Unread {
		t.Errorf("item %s Unread = false, want still true: reconcileSeenFlags must not cross UIDVALIDITY generations", staleGeneration.ID)
	}
	select {
	case ev := <-sink.markRead:
		t.Errorf("unexpected MarkRead call: %+v", ev)
	default:
	}
}

// TestAdapterRunPeriodicSeenReconcileAppliesChangeMadeElsewhere is the
// integration-level proof that Run wires the periodic ticker correctly:
// with a short SeenReconcileInterval, a \Seen change made by a second
// client while the adapter is connected and IDLEing eventually clears
// the item's unread state without Run ever returning an error or the
// connection dropping.
func TestAdapterRunPeriodicSeenReconcileAppliesChangeMadeElsewhere(t *testing.T) {
	addr, _, _ := newMemIMAPServer(t)
	appendMessage(t, addr, "INBOX", rawMessage(
		"<msg1@example.org>", "", "Read me on the phone",
		"Alice <alice@example.org>", "alice@example.org", "Body one",
	))

	cfg := AccountConfig{
		Name: "cl", IMAPHost: "unused", FolderPrefix: "INBOX", FolderSeparator: '.',
		SeenReconcileInterval: 20 * time.Millisecond,
	}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(addr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()

	seed := waitForUpsert(t, sink, 5*time.Second)

	_, _, uidValidity, uid, err := parseItemID(seed.ID)
	if err != nil {
		t.Fatalf("parseItemID(%q): %v", seed.ID, err)
	}
	markSeenElsewhere(t, addr, "INBOX", uid)
	_ = uidValidity

	deadline := time.Now().Add(5 * time.Second)
	for {
		item, err := sink.getItem(seed.ID)
		if err == nil && !item.Unread {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the periodic reconcile to clear %s (last item=%+v, err=%v)", seed.ID, item, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Run to return after cancel")
	}
}
