package rpc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// isZeroReceipt reports whether r carries no result at all — the zero
// Receipt a dry-run must return. Receipt now carries a Recipients slice
// (T13a), so plain struct comparison (r != core.Receipt{}) no longer
// compiles.
func isZeroReceipt(r core.Receipt) bool {
	return r.ID == "" && r.Channel == "" && r.At.IsZero() && len(r.Recipients) == 0
}

func startTestServer(t *testing.T) (*rpc.Client, *fake.Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	seed := core.Item{
		ID:      "mail:cl:1",
		Channel: core.ChannelMail,
		Account: "cl",
		From:    core.Address{ID: "them@x.cl"},
		Subject: "hi",
		Unread:  true,
	}
	if err := st.Upsert(context.Background(), seed); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	reg := core.NewRegistry()
	adapter := fake.New(core.ChannelMail, "cl", seed)
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})

	c := dialUntilReady(t, socket)
	t.Cleanup(func() { c.Close() })
	return c, adapter, socket
}

func TestClientListAndGet(t *testing.T) {
	client, _, _ := startTestServer(t)
	ctx := context.Background()

	items, err := client.List(ctx, core.Filter{Channel: core.ChannelMail})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != "mail:cl:1" {
		t.Fatalf("List = %+v, want one mail:cl:1 item", items)
	}

	item, err := client.Get(ctx, "mail:cl:1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.Subject != "hi" {
		t.Fatalf("Get.Subject = %q, want hi", item.Subject)
	}
}

func TestClientGetUnknownReturnsErrNotFound(t *testing.T) {
	client, _, _ := startTestServer(t)
	_, err := client.Get(context.Background(), "mail:cl:missing")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestClientCounts(t *testing.T) {
	client, _, _ := startTestServer(t)
	counts, err := client.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts[core.ChannelMail]["cl"] != 1 {
		t.Fatalf("counts = %+v, want mail/cl=1", counts)
	}
}

// TestClientReadFetchesOverSocket covers T13(c)'s RPC wiring: the "read"
// method round-trips through the server to core.Service.Read. The fake
// adapter here implements no ReadMarker, so markReceipt=true is a no-op
// (mail's own behavior); this test only proves the plumbing reaches
// Service.Read and back, not the mark-read side effect itself (covered
// in internal/core/fanout_test.go and the whatsapp/matrix packages).
func TestClientReadFetchesOverSocket(t *testing.T) {
	client, _, _ := startTestServer(t)
	item, err := client.Read(context.Background(), "mail:cl:1", true)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if item.ID != "mail:cl:1" {
		t.Fatalf("Read = %+v, want mail:cl:1", item)
	}
}

func TestClientReplyDryRunNeverReachesAdapter(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	plan, receipt, err := client.Reply(context.Background(), "mail:cl:1", "reply body", nil, nil, true)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if len(adapter.SentMessages()) != 0 {
		t.Fatalf("dry-run reached adapter: %+v", adapter.SentMessages())
	}
	if !isZeroReceipt(receipt) {
		t.Fatalf("dry-run receipt not empty: %+v", receipt)
	}
	if plan.Action != "reply" {
		t.Fatalf("plan.Action = %q, want reply", plan.Action)
	}
}

func TestClientReplyExecutesOverSocket(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	_, receipt, err := client.Reply(context.Background(), "mail:cl:1", "reply body", nil, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if receipt.ID == "" {
		t.Fatalf("expected non-empty receipt id")
	}
	sent := adapter.SentMessages()
	if len(sent) != 1 || sent[0].Body != "reply body" {
		t.Fatalf("SentMessages = %+v", sent)
	}
}

// TestClientReplyWithCcReachesAdapterOverSocket covers T12(b): Cc must
// round-trip through the JSON-RPC wire, not just the in-process Service.
func TestClientReplyWithCcReachesAdapterOverSocket(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	_, _, err := client.Reply(context.Background(), "mail:cl:1", "reply body", []string{"cc@x.cl"}, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	sent := adapter.SentMessages()
	if len(sent) != 1 || len(sent[0].Cc) != 1 || sent[0].Cc[0] != "cc@x.cl" {
		t.Fatalf("SentMessages = %+v, want one message with Cc [cc@x.cl]", sent)
	}
}

func TestClientSendOrganizeAndPostStatus(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	ctx := context.Background()

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"x@y.cl"}, Cc: []string{"cc@y.cl"}, Body: "fresh"}
	_, receipt, err := client.Send(ctx, out, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if receipt.ID == "" {
		t.Fatal("expected non-empty send receipt id")
	}
	// sendParams embeds core.Outgoing, so Cc round-trips over the wire
	// automatically (T12: verify this holds, not just assume it).
	sent := adapter.SentMessages()
	if len(sent) != 1 || len(sent[0].Cc) != 1 || sent[0].Cc[0] != "cc@y.cl" {
		t.Fatalf("SentMessages = %+v, want one message with Cc [cc@y.cl]", sent)
	}

	plan, err := client.Organize(ctx, "mail:cl:1", core.OrganizeOp{AddLabels: []string{"vip"}}, false)
	if err != nil {
		t.Fatalf("Organize: %v", err)
	}
	if plan.Action != "organize" {
		t.Fatalf("plan.Action = %q, want organize", plan.Action)
	}
	if len(adapter.OrganizeCalls()) != 1 {
		t.Fatalf("OrganizeCalls = %+v, want 1", adapter.OrganizeCalls())
	}

	_, statusReceipt, err := client.PostStatus(ctx, core.ChannelMail, "cl", core.Status{Text: "hola"}, false)
	if err != nil {
		t.Fatalf("PostStatus: %v", err)
	}
	if statusReceipt.ID == "" {
		t.Fatal("expected non-empty status receipt id")
	}
}

// TestClientDownloadWritesFileViaDaemon proves the download RPC method
// makes the daemon write the attachment straight to destPath on its own
// filesystem, rather than streaming the bytes back over the socket (see
// core.Service.Download and the design note in internal/rpc/protocol.go).
func TestClientDownloadWritesFileViaDaemon(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	data := []byte("attachment bytes over rpc")
	item := core.Item{
		ID:          "mail:cl:att",
		Channel:     core.ChannelMail,
		Account:     "cl",
		Attachments: []core.Attachment{{Name: "a.txt", MIME: "text/plain", Size: int64(len(data))}},
	}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	reg := core.NewRegistry()
	adapter := fake.New(core.ChannelMail, "cl")
	adapter.SetAttachmentData(item.ID, 0, data)
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, socket)

	client := dialUntilReady(t, socket)
	defer client.Close()

	dest := filepath.Join(dir, "out.txt")
	res, err := client.Download(context.Background(), item.ID, 0, dest, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Bytes != int64(len(data)) || res.Path != dest {
		t.Fatalf("Download result = %+v, want Bytes=%d Path=%s", res, len(data), dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", dest, err)
	}
	if string(got) != string(data) {
		t.Fatalf("file contents = %q, want %q", got, data)
	}
}

func TestClientReplyUnsupportedCapabilityReturnsErrUnsupported(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	item := core.Item{ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	reg := core.NewRegistry() // no adapter registered at all
	svc := core.NewService(st, reg)
	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, socket)

	client := dialUntilReady(t, socket)
	defer client.Close()

	_, _, err = client.Reply(context.Background(), item.ID, "x", nil, nil, true)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestClientAvatarWritesGeneratedFallbackViaDaemon proves the avatar RPC
// method reaches core.Service.Avatar and returns a path the daemon itself
// wrote (like download, avatar bytes never round-trip the socket).
func TestClientAvatarWritesGeneratedFallbackViaDaemon(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	reg := core.NewRegistry()
	adapter := fake.New(core.ChannelMail, "cl") // no AvatarProvider capability
	reg.Register(adapter)
	svc := core.NewService(st, reg)
	svc.SetAvatarCacheDir(filepath.Join(dir, "avatars"))

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, socket)

	client := dialUntilReady(t, socket)
	defer client.Close()

	res, err := client.Avatar(context.Background(), core.ChannelMail, "cl", "thread-1")
	if err != nil {
		t.Fatalf("Avatar: %v", err)
	}
	if !res.Generated {
		t.Errorf("Generated = false, want true (mail has no AvatarProvider)")
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("avatar file %s does not exist: %v", res.Path, err)
	}
}

// TestClientThreadReturnsOldestFirstOverSocket proves the thread RPC
// method reaches core.Service.Thread/store.Store.Thread and comes back
// oldest→newest, scoped to one conversation, over the real socket
// protocol (not just the in-process fakes rpc_test's other cases use for
// List/Get).
func TestClientThreadReturnsOldestFirstOverSocket(t *testing.T) {
	client, _, socket := startTestServer(t)
	ctx := context.Background()

	// startTestServer already seeded "mail:cl:1" with no Thread; add a
	// real conversation directly on the same on-disk store so the
	// running daemon (already serving that store) sees these rows too.
	st, err := store.Open(filepath.Join(filepath.Dir(socket), "bunker.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()

	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	thread := "5511999999999@s.whatsapp.net"
	for i, id := range []string{"whatsapp:personal:1", "whatsapp:personal:2", "whatsapp:personal:3"} {
		item := core.Item{
			ID: id, Channel: core.ChannelWhatsApp, Account: "personal", Thread: thread,
			Body: id, Timestamp: base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.Upsert(ctx, item); err != nil {
			t.Fatalf("seed thread item %s: %v", id, err)
		}
	}

	items, err := client.Thread(ctx, "whatsapp", "personal", thread, time.Time{}, 10)
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("Thread() len = %d, want 3", len(items))
	}
	wantOrder := []string{"whatsapp:personal:1", "whatsapp:personal:2", "whatsapp:personal:3"}
	for i, want := range wantOrder {
		if items[i].ID != want {
			t.Fatalf("Thread()[%d].ID = %q, want %q (oldest→newest)", i, items[i].ID, want)
		}
	}
}

// TestClientReadThreadMarksUnreadItemsAndSetsSeenOverSocket proves the
// read_thread RPC method reaches core.Service.ReadThread over the real
// socket protocol, and that mail's Organizer fallback (fake.Adapter
// implements only core.Organizer, like the real mail adapter) sets \Seen
// on each unread item — the K9 read-on-open fix (conversation-view.md).
func TestClientReadThreadMarksUnreadItemsAndSetsSeenOverSocket(t *testing.T) {
	client, adapter, socket := startTestServer(t)
	ctx := context.Background()

	st, err := store.Open(filepath.Join(filepath.Dir(socket), "bunker.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st.Close()

	base := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	thread := "them@x.cl"
	unreadIDs := []string{"mail:cl:th1", "mail:cl:th2"}
	for i, id := range unreadIDs {
		item := core.Item{
			ID: id, Channel: core.ChannelMail, Account: "cl", Thread: thread,
			Unread: true, Timestamp: base.Add(time.Duration(i) * time.Minute),
		}
		if err := st.Upsert(ctx, item); err != nil {
			t.Fatalf("seed thread item %s: %v", id, err)
		}
	}
	fromMe := core.Item{
		ID: "mail:cl:th3", Channel: core.ChannelMail, Account: "cl", Thread: thread,
		Unread: true, FromMe: true, Timestamp: base.Add(2 * time.Minute),
	}
	if err := st.Upsert(ctx, fromMe); err != nil {
		t.Fatalf("seed FromMe item: %v", err)
	}

	count, err := client.ReadThread(ctx, "mail", "cl", thread, true)
	if err != nil {
		t.Fatalf("ReadThread: %v", err)
	}
	if count != 2 {
		t.Fatalf("ReadThread() count = %d, want 2", count)
	}

	calls := adapter.OrganizeCalls()
	if len(calls) != 2 {
		t.Fatalf("OrganizeCalls = %+v, want 2", calls)
	}
	for i, want := range unreadIDs {
		if calls[i].ID != want {
			t.Errorf("OrganizeCalls[%d].ID = %q, want %q", i, calls[i].ID, want)
		}
		if calls[i].Op.Seen == nil || !*calls[i].Op.Seen {
			t.Errorf("OrganizeCalls[%d].Op.Seen = %v, want true", i, calls[i].Op.Seen)
		}
	}

	for _, id := range unreadIDs {
		item, err := client.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		if item.Unread {
			t.Errorf("Get(%s).Unread = true, want false", id)
		}
	}
	if item, err := client.Get(ctx, fromMe.ID); err != nil || !item.Unread {
		t.Errorf("Get(FromMe item) = (%+v, %v), want Unread=true untouched", item, err)
	}
}

// presenceCapableAdapter is a minimal core.Adapter implementing
// PresenceProvider, PresenceAvailabilityController and TypingSender, so
// rpc_test.go can prove the presence/presence_keepalive/typing RPC
// methods actually reach core.Service over the real socket protocol,
// not just the in-process fakes rpc_test's other cases use.
type presenceCapableAdapter struct {
	channel core.Channel
	account string

	mu            sync.Mutex
	presenceCalls []struct {
		available bool
		thread    string
	}
	typingCalls []struct {
		thread    string
		composing bool
	}
	presenceResult core.Presence
}

func (p *presenceCapableAdapter) Channel() core.Channel                { return p.channel }
func (p *presenceCapableAdapter) Account() string                      { return p.account }
func (p *presenceCapableAdapter) Run(context.Context, core.Sink) error { return nil }

func (p *presenceCapableAdapter) SetPresenceAvailable(_ context.Context, available bool, thread string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.presenceCalls = append(p.presenceCalls, struct {
		available bool
		thread    string
	}{available, thread})
	return nil
}

func (p *presenceCapableAdapter) Presence(context.Context, string) (core.Presence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.presenceResult, nil
}

func (p *presenceCapableAdapter) SendTyping(_ context.Context, thread string, composing bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.typingCalls = append(p.typingCalls, struct {
		thread    string
		composing bool
	}{thread, composing})
	return nil
}

// TestClientPresenceTypingAndKeepaliveOverSocket proves the presence,
// presence_keepalive and typing RPC methods reach core.Service (and, for
// presence_keepalive, the availability lease in internal/core/presence.go)
// over the real socket protocol.
func TestClientPresenceTypingAndKeepaliveOverSocket(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	reg := core.NewRegistry()
	adapter := &presenceCapableAdapter{
		channel: core.ChannelWhatsApp, account: "personal",
		presenceResult: core.Presence{State: "typing", Typers: []string{"5511999999999@s.whatsapp.net"}},
	}
	reg.Register(adapter)
	svc := core.NewService(st, reg)

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, socket)

	client := dialUntilReady(t, socket)
	defer client.Close()

	thread := "5511999999999@s.whatsapp.net"

	if err := client.PresenceKeepalive(context.Background(), "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("PresenceKeepalive(focused=true): %v", err)
	}
	if err := client.Typing(context.Background(), "whatsapp", "personal", thread, true); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	presence, err := client.Presence(context.Background(), "whatsapp", "personal", thread)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if presence.State != "typing" || len(presence.Typers) != 1 {
		t.Fatalf("Presence = %+v, want State=typing with one typer", presence)
	}
	if err := client.PresenceKeepalive(context.Background(), "whatsapp", "personal", thread, false); err != nil {
		t.Fatalf("PresenceKeepalive(focused=false): %v", err)
	}

	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(adapter.presenceCalls) != 2 || !adapter.presenceCalls[0].available || adapter.presenceCalls[1].available {
		t.Fatalf("presenceCalls = %+v, want [available=true, available=false]", adapter.presenceCalls)
	}
	if len(adapter.typingCalls) != 1 || !adapter.typingCalls[0].composing || adapter.typingCalls[0].thread != thread {
		t.Fatalf("typingCalls = %+v, want one composing=true call for %q", adapter.typingCalls, thread)
	}
}

// TestClientHealthReturnsTrackerSnapshotOverSocket proves R4's health RPC
// method: Client.Health round-trips through the real socket protocol to
// whatever core.Service.Health (backed by a wired HealthTracker) reports.
func TestClientHealthReturnsTrackerSnapshotOverSocket(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	svc := core.NewService(st, core.NewRegistry())
	tracker := core.NewHealthTracker()
	tracker.SetConnected(core.ChannelMail, "cl", time.Unix(1, 0))
	svc.SetHealthTracker(tracker)

	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, socket)

	client := dialUntilReady(t, socket)
	defer client.Close()

	adapters, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if len(adapters) != 1 || adapters[0].Channel != core.ChannelMail || adapters[0].Account != "cl" || adapters[0].State != core.AdapterConnected {
		t.Fatalf("Health() = %+v, want one connected mail/cl entry", adapters)
	}
}

// dialUntilReady polls until the server at socket accepts a connection.
// Serve gives no readiness signal, so polling is the only option; the
// budget is generous because the pure-Go SQLite store opens slowly under
// -race on a busy CI runner, and a timeout fails loudly.
func dialUntilReady(t *testing.T, socket string) *rpc.Client {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil
}

// TestClientReplyPlanAndReceiptSurviveTheWire covers the snake_case
// Plan/Receipt tags (issue #68): the daemon and the client are the same
// binary, but every field must still round-trip through the socket.
func TestClientReplyPlanAndReceiptSurviveTheWire(t *testing.T) {
	client, _, _ := startTestServer(t)
	plan, receipt, err := client.Reply(context.Background(), "mail:cl:1", "reply body", []string{"cc@x.cl"}, nil, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if plan.Action != "reply" || plan.Channel != core.ChannelMail || plan.Account != "cl" || plan.Preview != "reply body" {
		t.Fatalf("plan = %+v, want reply/mail/cl/reply body", plan)
	}
	if len(plan.Cc) != 1 || plan.Cc[0] != "cc@x.cl" || len(plan.Recipients) == 0 || plan.Target == "" {
		t.Fatalf("plan = %+v, want Cc, Recipients and Target to survive", plan)
	}
	if receipt.ID == "" || receipt.Channel != core.ChannelMail || receipt.At.IsZero() {
		t.Fatalf("receipt = %+v, want id, channel and at", receipt)
	}
}
