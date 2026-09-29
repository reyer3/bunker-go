package rpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

// startChatServer serves a daemon with one fake chat account holding one
// message of ours and one of theirs, for the edit/delete/react round
// trips.
func startChatServer(t *testing.T) (*rpc.Client, *fake.Adapter, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, it := range []core.Item{
		{ID: "whatsapp:personal:chat/MINE", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "hola", FromMe: true, Timestamp: time.Now()},
		{ID: "whatsapp:personal:chat/THEIRS", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "chat", Body: "que tal", Timestamp: time.Now()},
	} {
		if err := st.Upsert(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	reg := core.NewRegistry()
	adapter := fake.New(core.ChannelWhatsApp, "personal")
	reg.Register(adapter)
	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(core.NewService(st, reg))
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := rpc.Dial(socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c, adapter, st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil, nil, nil
}

func TestClientEditDeleteReactRoundTrip(t *testing.T) {
	client, adapter, st := startChatServer(t)
	ctx := context.Background()

	plan, receipt, err := client.EditMessage(ctx, "whatsapp:personal:chat/MINE", "hola!", true)
	if err != nil || plan.Action != "edit" || plan.Preview != "hola!" || !isZeroReceipt(receipt) {
		t.Fatalf("edit dry-run = %+v, %+v, %v", plan, receipt, err)
	}
	if len(adapter.MessageActions()) != 0 {
		t.Fatal("a dry-run reached the adapter")
	}
	if _, receipt, err = client.EditMessage(ctx, "whatsapp:personal:chat/MINE", "hola!", false); err != nil || receipt.ID == "" {
		t.Fatalf("edit = %+v, %v", receipt, err)
	}
	if _, _, err := client.React(ctx, "whatsapp:personal:chat/THEIRS", "👍", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.DeleteMessage(ctx, "whatsapp:personal:chat/MINE", false); err != nil {
		t.Fatal(err)
	}
	want := []fake.MessageAction{
		{Action: "edit", ID: "whatsapp:personal:chat/MINE", Text: "hola!"},
		{Action: "react", ID: "whatsapp:personal:chat/THEIRS", Text: "👍"},
		{Action: "delete", ID: "whatsapp:personal:chat/MINE"},
	}
	got := adapter.MessageActions()
	if len(got) != len(want) {
		t.Fatalf("actions = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("action %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	mine, _ := st.Get(ctx, "whatsapp:personal:chat/MINE")
	theirs, _ := st.Get(ctx, "whatsapp:personal:chat/THEIRS")
	if !mine.Deleted || !mine.Edited {
		t.Errorf("our message = %+v, want edited then deleted", mine)
	}
	if len(theirs.Reactions) != 1 || theirs.Reactions[0] != (core.Reaction{Sender: fake.ReactionSender, Emoji: "👍"}) {
		t.Errorf("their message's reactions = %+v", theirs.Reactions)
	}
}

func TestClientEditErrorsKeepTheirSentinels(t *testing.T) {
	client, _, _ := startChatServer(t)
	ctx := context.Background()
	if _, _, err := client.EditMessage(ctx, "whatsapp:personal:chat/NOPE", "x", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
	if _, _, err := client.DeleteMessage(ctx, "whatsapp:personal:chat/THEIRS", true); err == nil {
		t.Error("deleting their message succeeded")
	}
}

func TestClientReactCarriesIdempotencyKey(t *testing.T) {
	client, adapter, _ := startChatServer(t)
	ctx := core.WithIdempotencyKey(context.Background(), "react-1")
	if _, _, err := client.React(ctx, "whatsapp:personal:chat/THEIRS", "🙏", false); err != nil {
		t.Fatal(err)
	}
	_, again, err := client.React(ctx, "whatsapp:personal:chat/THEIRS", "🙏", false)
	if err != nil || !again.Replayed {
		t.Fatalf("repeat = %+v, %v; want a replay", again, err)
	}
	if n := len(adapter.MessageActions()); n != 1 {
		t.Errorf("reacted %d times, want 1", n)
	}
}
