package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestSendAndReplyPassIdempotencyKey(t *testing.T) {
	backend := &fakeBackend{
		items:   map[string]core.Item{"mail:cl:1": {ID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"}},
		receipt: core.Receipt{ID: "R1", Replayed: true},
	}
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"send", "mail", "cl", "a@example.org", "hola", "--idempotency-key", "k1"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("send exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "nothing was sent again") {
		t.Errorf("a replayed receipt should say so: %q", stdout.String())
	}
	code = runWithBackend(context.Background(), backend, []string{"reply", "mail:cl:1", "ok", "--idempotency-key", "k2"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reply exit %d: %s", code, stderr.String())
	}
	code = runWithBackend(context.Background(), backend, []string{"send", "mail", "cl", "a@example.org", "hola"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("send exit %d: %s", code, stderr.String())
	}
	if got := strings.Join(backend.sendKeys, ","); got != "k1,k2," {
		t.Fatalf("keys = %q, want k1,k2 and none for the plain send", got)
	}
}
