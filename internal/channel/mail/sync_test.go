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
			return memServer.NewSession(), nil, nil
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

func (s *fakeSink) snapshot() []core.Item {
	s.lock()
	defer s.unlock()
	out := make([]core.Item, 0, len(s.items))
	for _, it := range s.items {
		out = append(out, it)
	}
	return out
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
