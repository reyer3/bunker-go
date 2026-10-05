package rpc_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

func TestClientAwaitingReplyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sent := time.Now().Add(-5 * 24 * time.Hour).Truncate(time.Second)
	for _, it := range []core.Item{
		{ID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "demo-ana",
			ThreadName: "Demo Ana", FromMe: true, Body: "¿Me confirmas la hora?", Timestamp: sent},
		{ID: "whatsapp:personal:B1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "demo-beto",
			ThreadName: "Demo Beto", FromMe: true, Body: "Hola", Timestamp: time.Now()},
	} {
		if err := st.Upsert(context.Background(), it); err != nil {
			t.Fatal(err)
		}
	}
	svc := core.NewService(st, core.NewRegistry())
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- rpc.NewServer(svc).Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})
	client := dialUntilReady(t, socket)
	t.Cleanup(func() { client.Close() })

	got, err := client.AwaitingReply(ctx, core.AwaitingFilter{Days: 3})
	if err != nil {
		t.Fatalf("AwaitingReply: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %+v, want only the 5-day-old chat", got)
	}
	a := got[0]
	if a.ItemID != "whatsapp:personal:A1" || a.Person != "Demo Ana" || a.Preview != "¿Me confirmas la hora?" ||
		a.Days != 5 || !a.Sent.Equal(sent) {
		t.Errorf("row = %+v", a)
	}
}
