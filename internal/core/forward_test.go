package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func forwardOut() core.Outgoing {
	return core.Outgoing{
		Channel: core.ChannelWhatsApp,
		Account: "personal",
		To:      []string{"1234@s.whatsapp.net"},
		Body:    "mensaje reenviado",
		Forward: true,
	}
}

// TestServiceSendForwardReachesAdapterAndPlan: a forward is an ordinary
// send whose marker reaches the adapter (which may label it natively) and
// shows in the Plan, so a dry-run says it would go out as a forward.
func TestServiceSendForwardReachesAdapterAndPlan(t *testing.T) {
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(), reg)

	plan, _, err := svc.Send(context.Background(), forwardOut(), true)
	if err != nil {
		t.Fatalf("dry-run Send: %v", err)
	}
	if !plan.Forward {
		t.Error("dry-run plan.Forward = false, want true")
	}
	if spy.sendCalls != 0 {
		t.Fatalf("dry-run reached the adapter %d times", spy.sendCalls)
	}

	plan, _, err = svc.Send(context.Background(), forwardOut(), false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !plan.Forward {
		t.Error("plan.Forward = false, want true")
	}
	if !spy.lastOutgoing.Forward {
		t.Error("adapter Outgoing.Forward = false, want true")
	}
}

// TestServiceSendForwardRejectsSeveralRecipients: forwarding is one
// target at a time (human pacing on chat channels); a broadcast forward
// is refused before anything is sent.
func TestServiceSendForwardRejectsSeveralRecipients(t *testing.T) {
	reg := core.NewRegistry()
	spy := &spyAdapter{channel: core.ChannelWhatsApp, account: "personal"}
	reg.Register(spy)
	svc := core.NewService(newMemStore(), reg)

	out := forwardOut()
	out.To = []string{"1111@s.whatsapp.net", "2222@s.whatsapp.net"}
	for _, dryRun := range []bool{true, false} {
		_, _, err := svc.Send(context.Background(), out, dryRun)
		if !errors.Is(err, core.ErrUnsupported) {
			t.Fatalf("dryRun=%v: err = %v, want ErrUnsupported", dryRun, err)
		}
	}
	if spy.sendCalls != 0 {
		t.Fatalf("sent %d times, want 0", spy.sendCalls)
	}
}

// TestIdempotencyForwardIsPartOfTheMessage: the same text sent once as a
// forward and once plain under one key is a different message, not a
// retry.
func TestIdempotencyForwardIsPartOfTheMessage(t *testing.T) {
	sender := &gatedSender{}
	svc := idempotencyService(t, sender)
	ctx := core.WithIdempotencyKey(context.Background(), "k-forward")
	if _, _, err := svc.Send(ctx, hola(), false); err != nil {
		t.Fatal(err)
	}
	fwd := hola()
	fwd.Forward = true
	_, _, err := svc.Send(ctx, fwd, false)
	if err == nil || !strings.Contains(err.Error(), "different message") {
		t.Fatalf("err = %v, want a loud mismatch", err)
	}
}
