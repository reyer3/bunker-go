package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestCmdSendForwardMarksOutgoing(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"send", "whatsapp", "personal", "5511999", "hola", "--forward"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.sendCalls) != 1 || !backend.sendCalls[0].Forward {
		t.Fatalf("sendCalls = %+v, want one forward", backend.sendCalls)
	}
}

// TestCmdSendForwardDryRunSaysSo: the human dry-run output tells the user
// the message would go out as a forward.
func TestCmdSendForwardDryRunSaysSo(t *testing.T) {
	backend := newFakeBackend()
	backend.sendPlan = core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "personal", Recipients: []string{"5511999"}, Preview: "hola", Forward: true}
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"send", "whatsapp", "personal", "5511999", "hola", "--forward", "--dry-run"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "as a forward") {
		t.Fatalf("stdout = %q, want it to say the send is a forward", stdout.String())
	}
}

func TestMCPSendForwardShowsInPlan(t *testing.T) {
	_, call := mediaServiceSession(t, false)
	isErr, out, text := call("send", map[string]any{
		"channel": "whatsapp", "account": "personal", "to": "51900@s.whatsapp.net",
		"text": "te lo reenvío", "forward": true,
	})
	if isErr || out["sent"] != false {
		t.Fatalf("plan: %v %s", out, text)
	}
	plan, _ := out["plan"].(map[string]any)
	if plan["forward"] != true {
		t.Fatalf("plan = %v, want forward: true", plan)
	}
}

// TestMCPIdempotencyKeyTellsAForwardApart: the same text sent plain and
// as a forward are two sends, not a retry of one.
func TestMCPIdempotencyKeyTellsAForwardApart(t *testing.T) {
	plain := core.Plan{Action: "send", Channel: core.ChannelWhatsApp, Account: "personal", Recipients: []string{"5511999"}, Preview: "hola"}
	fwd := plain
	fwd.Forward = true
	if mcpIdempotencyKey(plain) == mcpIdempotencyKey(fwd) {
		t.Fatal("a forward and a plain send share an idempotency key")
	}
}
