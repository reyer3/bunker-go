package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func awaitingBackend() *fakeBackend {
	b := newFakeBackend()
	b.awaiting = []core.Awaiting{
		{ItemID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "demo-ana",
			Person: "Demo Ana", Preview: "te paso el informe", Days: 4,
			Sent: time.Date(2026, 10, 1, 9, 30, 0, 0, time.Local)},
	}
	return b
}

func TestCmdAwaiting(t *testing.T) {
	b := awaitingBackend()
	code, out, stderr := runCallCmd(t, b, "awaiting")
	if code != 0 {
		t.Fatalf("code = %d stderr = %q", code, stderr)
	}
	if len(b.awaitingCalls) != 1 || b.awaitingCalls[0].Days != core.DefaultAwaitingDays || b.awaitingCalls[0].Groups {
		t.Fatalf("calls = %+v, want default days and no groups", b.awaitingCalls)
	}
	for _, want := range []string{"4 d", "Demo Ana", "te paso el informe", "whatsapp:personal:A1", "2026-10-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want %q", out, want)
		}
	}

	code, out, _ = runCallCmd(t, b, "awaiting", "--days", "7", "--groups", "--json")
	if code != 0 {
		t.Fatalf("json code = %d", code)
	}
	if got := b.awaitingCalls[1]; got.Days != 7 || !got.Groups {
		t.Errorf("flags = %+v", got)
	}
	var res struct {
		Awaiting []core.Awaiting `json:"awaiting"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Awaiting) != 1 || res.Awaiting[0].Person != "Demo Ana" {
		t.Fatalf("json = %q, %v", out, err)
	}
}

func TestCmdAwaitingEmptyAndUsage(t *testing.T) {
	b := newFakeBackend()
	code, out, _ := runCallCmd(t, b, "awaiting", "--json")
	if code != 0 || !strings.Contains(out, `"awaiting":[]`) {
		t.Errorf("empty json: code=%d out=%q", code, out)
	}
	code, out, _ = runCallCmd(t, b, "awaiting")
	if code != 0 || !strings.Contains(out, "no conversations awaiting a reply") {
		t.Errorf("empty text: code=%d out=%q", code, out)
	}
	for _, args := range [][]string{{"awaiting", "--days", "0"}, {"awaiting", "extra"}} {
		if code, _, _ := runCallCmd(t, b, args...); code != 2 {
			t.Errorf("%v code = %d, want 2", args, code)
		}
	}
}
