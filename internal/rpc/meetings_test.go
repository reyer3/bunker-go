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

func TestClientMeetingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	meta, err := core.MeetingMeta(nil, core.Meeting{
		UID: "u1", Method: "REQUEST", Summary: "Revisión semanal",
		Start: now.Add(30 * time.Minute), End: now.Add(90 * time.Minute), URL: "https://meet.google.com/abc-defg-hij",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(context.Background(), core.Item{ID: "mail:cl:9", Channel: core.ChannelMail, Account: "cl", Timestamp: now, Meta: meta}); err != nil {
		t.Fatal(err)
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

	got, err := client.Meetings(ctx, core.MeetingFilter{Days: 2})
	if err != nil {
		t.Fatalf("Meetings: %v", err)
	}
	if len(got) != 1 || got[0].ItemID != "mail:cl:9" || got[0].Summary != "Revisión semanal" ||
		got[0].URL != "https://meet.google.com/abc-defg-hij" || !got[0].Start.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("Meetings = %+v, want the stored invitation to survive the wire", got)
	}
}
