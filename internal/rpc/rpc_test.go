package rpc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	// Wait for the socket file to appear instead of a fixed sleep.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c, adapter, socket
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil, nil, ""
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

	var client *rpc.Client
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			client = c
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("server never became reachable")
	}
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

	var client *rpc.Client
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			client = c
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("server never became reachable")
	}
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

	var client *rpc.Client
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			client = c
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("server never became reachable")
	}
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
