package rpc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
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
