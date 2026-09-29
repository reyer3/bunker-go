package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

const ownID = "whatsapp:personal:chat/MINE"

func runModify(t *testing.T, backend *fakeBackend, stdin string, tty bool, args ...string) (int, string, string) {
	t.Helper()
	prev := stdinIsTerminal
	stdinIsTerminal = func(io.Reader) bool { return tty }
	t.Cleanup(func() { stdinIsTerminal = prev })
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestCmdEditDryRunAndReal(t *testing.T) {
	b := newFakeBackend()
	code, out, _ := runModify(t, b, "", false, "edit", ownID, "hola!", "--dry-run")
	if code != 0 || !strings.Contains(out, "[dry-run] would edit "+ownID) || !strings.Contains(out, "hola!") {
		t.Fatalf("dry-run: code %d, out %q", code, out)
	}
	code, out, _ = runModify(t, b, "", false, "edit", ownID, "hola!", "--idempotency-key", "k", "--json")
	if code != 0 {
		t.Fatalf("edit: code %d", code)
	}
	var res struct {
		DryRun  bool
		Plan    core.Plan
		Receipt core.Receipt
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Plan.Action != "edit" || res.Receipt.ID != "fake-receipt" {
		t.Fatalf("json = %q (%v)", out, err)
	}
	want := []messageActionCall{
		{Action: "edit", ID: ownID, Text: "hola!", DryRun: true},
		{Action: "edit", ID: ownID, Text: "hola!", Key: "k"},
	}
	if len(b.actionCalls) != 2 || b.actionCalls[0] != want[0] || b.actionCalls[1] != want[1] {
		t.Errorf("calls = %+v, want %+v", b.actionCalls, want)
	}
}

func TestCmdDeleteRefusesWithoutYesOffTerminal(t *testing.T) {
	b := newFakeBackend()
	code, _, stderr := runModify(t, b, "", false, "delete", ownID)
	if code != 1 || !strings.Contains(stderr, "--yes") {
		t.Fatalf("code %d, stderr %q; want a refusal naming --yes", code, stderr)
	}
	for _, c := range b.actionCalls {
		if !c.DryRun {
			t.Fatalf("a real delete ran without --yes: %+v", b.actionCalls)
		}
	}

	code, out, _ := runModify(t, b, "", false, "delete", ownID, "--yes")
	if code != 0 || !strings.Contains(out, "delete ok: "+ownID) {
		t.Fatalf("--yes: code %d, out %q", code, out)
	}
	if last := b.actionCalls[len(b.actionCalls)-1]; last.DryRun || last.Action != "delete" {
		t.Errorf("last call = %+v, want the real delete", last)
	}
}

func TestCmdDeleteAsksOnTerminal(t *testing.T) {
	b := newFakeBackend()
	b.items[ownID] = core.Item{ID: ownID, Body: "ups"}

	code, _, stderr := runModify(t, b, "n\n", true, "delete", ownID)
	if code != 1 || !strings.Contains(stderr, `"ups"`) || !strings.Contains(stderr, "not deleted") {
		t.Fatalf("declined: code %d, stderr %q", code, stderr)
	}
	if n := len(b.actionCalls); n != 1 || !b.actionCalls[0].DryRun {
		t.Fatalf("calls after declining = %+v, want the plan only", b.actionCalls)
	}

	code, out, _ := runModify(t, b, "s\n", true, "delete", ownID)
	if code != 0 || !strings.Contains(out, "delete ok") {
		t.Fatalf("confirmed: code %d, out %q", code, out)
	}
	if last := b.actionCalls[len(b.actionCalls)-1]; last.DryRun {
		t.Error("confirming did not delete")
	}
}

func TestCmdDeleteDryRunNeverDeletes(t *testing.T) {
	b := newFakeBackend()
	code, out, _ := runModify(t, b, "", false, "delete", ownID, "--dry-run")
	if code != 0 || !strings.Contains(out, "[dry-run] would delete") {
		t.Fatalf("code %d, out %q", code, out)
	}
	if len(b.actionCalls) != 1 || !b.actionCalls[0].DryRun {
		t.Errorf("calls = %+v", b.actionCalls)
	}
}

func TestCmdReactAndRemove(t *testing.T) {
	b := newFakeBackend()
	if code, _, _ := runModify(t, b, "", false, "react", ownID, "👍"); code != 0 {
		t.Fatalf("react: code %d", code)
	}
	code, out, _ := runModify(t, b, "", false, "react", ownID, "--remove", "--dry-run")
	if code != 0 || !strings.Contains(out, "remove our reaction") {
		t.Fatalf("remove: code %d, out %q", code, out)
	}
	if b.actionCalls[0].Text != "👍" || b.actionCalls[1].Text != "" {
		t.Errorf("calls = %+v", b.actionCalls)
	}
	for _, bad := range [][]string{{"react", ownID}, {"react", ownID, "👍", "--remove"}} {
		if code, _, _ := runModify(t, b, "", false, bad...); code != 2 {
			t.Errorf("%v: code %d, want usage error 2", bad, code)
		}
	}
}
