package rpc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

func TestClientCallMethods(t *testing.T) {
	client, _, _ := startTestServer(t)
	ctx := context.Background()

	calls, err := client.Calls(ctx)
	if err != nil || len(calls) != 0 {
		t.Fatalf("Calls = %+v, %v; want none", calls, err)
	}
	// The fake mail adapter is no core.Caller: the sentinel must survive
	// the wire so the CLI can tell "unsupported" apart from a failure.
	if _, _, err := client.PlaceCall(ctx, core.ChannelMail, "cl", "x@y.cl", true); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("PlaceCall = %v, want ErrUnsupported", err)
	}
	if _, _, err := client.ControlCall(ctx, "nope", core.CallHangup, false); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("ControlCall = %v, want ErrNotFound", err)
	}
}

// callingAdapter is a fake WhatsApp account that is a core.Caller with a
// fixed set of live calls.
type callingAdapter struct {
	*fake.Adapter
	active []core.Call
}

func (a *callingAdapter) CanCall() error { return nil }
func (a *callingAdapter) PlaceCall(context.Context, string) (core.Call, error) {
	return core.Call{}, core.ErrUnsupported
}
func (a *callingAdapter) ControlCall(context.Context, string, core.CallAction) (core.Call, error) {
	return core.Call{}, core.ErrNotFound
}
func (a *callingAdapter) ActiveCalls() []core.Call { return a.active }

func TestClientCallsCarryVideo(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	reg := core.NewRegistry()
	reg.Register(&callingAdapter{
		Adapter: fake.New(core.ChannelWhatsApp, "personal"),
		active: []core.Call{
			{ID: "VOICE", Channel: core.ChannelWhatsApp, Account: "personal", State: core.CallStateRinging},
			{ID: "VIDEO", Channel: core.ChannelWhatsApp, Account: "personal", State: core.CallStateRinging, Video: true},
		},
	})
	socket := filepath.Join(dir, "bunker.sock")
	srv := rpc.NewServer(core.NewService(st, reg))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, socket) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	client := dialUntilReady(t, socket)
	t.Cleanup(func() { client.Close() })

	calls, err := client.Calls(context.Background())
	if err != nil || len(calls) != 2 {
		t.Fatalf("Calls = %+v, %v", calls, err)
	}
	video := map[string]bool{}
	for _, c := range calls {
		video[c.ID] = c.Video
	}
	if video["VOICE"] || !video["VIDEO"] {
		t.Fatalf("video over the wire = %v, want only VIDEO marked", video)
	}
}
