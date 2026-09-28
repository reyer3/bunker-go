package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func runCallCmd(t *testing.T, backend Backend, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCmdCallPlaceDryRun(t *testing.T) {
	backend := &fakeBackend{}
	code, out, _ := runCallCmd(t, backend, "call", "whatsapp", "personal", "+51999888777", "--dry-run")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(backend.placeCallCalls) != 1 || !backend.placeCallCalls[0].DryRun || backend.placeCallCalls[0].To != "+51999888777" {
		t.Fatalf("PlaceCall calls = %+v", backend.placeCallCalls)
	}
	if !strings.Contains(out, "[dry-run] would call via whatsapp/personal: +51999888777") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestCmdCallControlJSON(t *testing.T) {
	backend := &fakeBackend{call: core.Call{ID: "IN1", Channel: core.ChannelWhatsApp, Account: "personal", Peer: "519@s.whatsapp.net", State: core.CallStateConnecting}}
	code, out, _ := runCallCmd(t, backend, "call", "answer", "IN1", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if len(backend.controlCallCalls) != 1 || backend.controlCallCalls[0].Action != core.CallAnswer || backend.controlCallCalls[0].ID != "IN1" {
		t.Fatalf("ControlCall calls = %+v", backend.controlCallCalls)
	}
	var got struct {
		Call core.Call
		Plan core.Plan
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Call.ID != "IN1" || got.Plan.Action != "call answer" {
		t.Fatalf("json = %q (%v)", out, err)
	}
}

func TestCmdCallUsage(t *testing.T) {
	for _, args := range [][]string{{"call"}, {"call", "mute", "IN1"}, {"call", "whatsapp"}} {
		if code, _, stderr := runCallCmd(t, &fakeBackend{}, args...); code != 2 || !strings.Contains(stderr, "usage: bunker call") {
			t.Fatalf("%v: exit %d stderr %q", args, code, stderr)
		}
	}
}

func TestCmdCalls(t *testing.T) {
	code, out, _ := runCallCmd(t, &fakeBackend{}, "calls")
	if code != 0 || strings.TrimSpace(out) != "no active calls" {
		t.Fatalf("empty: exit %d out %q", code, out)
	}
	backend := &fakeBackend{calls: []core.Call{{ID: "IN1", Channel: core.ChannelWhatsApp, Account: "personal", Direction: core.CallIncoming, Peer: "519@s.whatsapp.net", PeerName: "Ana", State: core.CallStateRinging}}}
	code, out, _ = runCallCmd(t, backend, "calls")
	if code != 0 || !strings.Contains(out, "IN1 whatsapp/personal incoming Ana (519@s.whatsapp.net) ringing") {
		t.Fatalf("exit %d out %q", code, out)
	}
	live := &fakeBackend{calls: []core.Call{{ID: "C2", Channel: core.ChannelWhatsApp, Account: "wa", Direction: core.CallOutgoing, Peer: "519@s.whatsapp.net", State: core.CallStateActive, ConnectedAt: time.Now().Add(-65 * time.Second)}}}
	if code, out, _ = runCallCmd(t, live, "calls"); code != 0 || !strings.Contains(out, "active 1:05") {
		t.Fatalf("live duration: exit %d out %q", code, out)
	}
	code, out, _ = runCallCmd(t, &fakeBackend{}, "calls", "--json")
	if code != 0 || strings.TrimSpace(out) != `{"calls":[]}` {
		t.Fatalf("json empty: %q", out)
	}
}

func TestCmdCallLatest(t *testing.T) {
	backend := &fakeBackend{calls: []core.Call{
		{ID: "old", Direction: core.CallIncoming, State: core.CallStateActive},
		{ID: "ring", Direction: core.CallIncoming, State: core.CallStateRinging},
	}}
	if code, _, stderr := runCallCmd(t, backend, "call", "answer", "latest"); code != 0 {
		t.Fatalf("answer latest: exit %d %s", code, stderr)
	}
	if got := backend.controlCallCalls[0].ID; got != "ring" {
		t.Errorf("answer latest picked %q, want the ringing call", got)
	}
	if code, _, _ := runCallCmd(t, backend, "call", "hangup", "latest"); code != 0 {
		t.Fatal("hangup latest failed")
	}
	if got := backend.controlCallCalls[1].ID; got != "ring" {
		t.Errorf("hangup latest picked %q, want the newest live call", got)
	}

	empty := &fakeBackend{}
	code, _, stderr := runCallCmd(t, empty, "call", "answer", "latest")
	if code == 0 || !strings.Contains(stderr, "no hay ninguna llamada entrante") {
		t.Errorf("answer latest with no call: exit %d, stderr %q", code, stderr)
	}
	if len(empty.controlCallCalls) != 0 {
		t.Error("nothing should be controlled when no call matches")
	}
}
