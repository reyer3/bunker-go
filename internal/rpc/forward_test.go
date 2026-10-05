package rpc_test

import (
	"context"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestForwardCrossesTheSocket: Outgoing.Forward reaches the daemon's
// Service on send, and the Plan it answers with says so.
func TestForwardCrossesTheSocket(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Body: "reenviado", Forward: true}

	plan, _, err := client.Send(context.Background(), out, true)
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	if !plan.Forward {
		t.Fatalf("plan = %+v, want Forward", plan)
	}
	if _, _, err := client.Send(context.Background(), out, false); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := adapter.SentMessages()
	if len(sent) != 1 || !sent[0].Forward {
		t.Fatalf("sent = %+v, want one forward", sent)
	}
}
