package rpc_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// TestVoiceNoteCrossesTheSocket checks both ways a voice note is asked
// for reach the daemon's Service: Outgoing.Voice on send, and the context
// marker (core.WithVoice) on reply, which keeps Reply's signature.
func TestVoiceNoteCrossesTheSocket(t *testing.T) {
	client, adapter, _ := startTestServer(t)
	path := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(path, 4*time.Second); err != nil {
		t.Fatal(err)
	}

	plan, _, err := client.Reply(core.WithVoice(context.Background()), "mail:cl:1", "", nil, []string{path}, true)
	if err != nil {
		t.Fatalf("Reply dry-run: %v", err)
	}
	if !plan.Voice || len(plan.Attachments) != 1 || plan.Attachments[0].DurationMS != 4000 {
		t.Fatalf("plan = %+v, want a 4 s voice note", plan)
	}
	if len(adapter.SentMessages()) != 0 {
		t.Fatal("dry-run reached the adapter")
	}

	if _, _, err := client.Reply(core.WithVoice(context.Background()), "mail:cl:1", "", nil, []string{path}, false); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	_, _, err = client.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Attachments: []string{path}, Voice: true,
	}, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := adapter.SentMessages()
	if len(sent) != 2 || !sent[0].Voice || !sent[1].Voice {
		t.Fatalf("sent = %+v, want two voice notes", sent)
	}

}
