package rpc_test

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestClientSendCarriesIdempotencyKey checks the key survives the socket:
// the client lifts it out of the context, the server puts it back, and
// the daemon sends once.
func TestClientSendCarriesIdempotencyKey(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	ctx := core.WithIdempotencyKey(context.Background(), "k1")
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"them@x.cl"}, Body: "hola"}

	_, first, err := client.Send(ctx, out, false)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := client.Send(ctx, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(adapter.SentMessages()); n != 1 {
		t.Fatalf("sent %d times, want 1", n)
	}
	if !second.Replayed || second.ID != first.ID {
		t.Fatalf("second receipt %+v should replay %+v", second, first)
	}

	replyCtx := core.WithIdempotencyKey(context.Background(), "r1")
	if _, _, err := client.Reply(replyCtx, "mail:cl:1", "ok", nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, r, err := client.Reply(replyCtx, "mail:cl:1", "ok", nil, nil, false); err != nil || !r.Replayed {
		t.Fatalf("reply repeat: %+v, %v", r, err)
	}
	if n := len(adapter.SentMessages()); n != 2 {
		t.Fatalf("sent %d times, want 2", n)
	}
}
