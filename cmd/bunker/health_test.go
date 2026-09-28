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

// TestCmdHealthJSON proves `bunker health --json` reports the backend's
// per-adapter health snapshot (R4).
func TestCmdHealthJSON(t *testing.T) {
	backend := newFakeBackend()
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	backend.health = []core.AdapterHealth{
		{Channel: core.ChannelMail, Account: "cl", State: core.AdapterConnected, Since: since, Restarts: 2},
		{Channel: core.ChannelWhatsApp, Account: "personal", State: core.AdapterBackoff, Since: since, LastError: "dial refused", Restarts: 5},
	}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"health", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}

	var got struct {
		Adapters []core.AdapterHealth `json:"adapters"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if len(got.Adapters) != 2 {
		t.Fatalf("adapters = %+v, want 2", got.Adapters)
	}
	if got.Adapters[1].State != core.AdapterBackoff || got.Adapters[1].LastError != "dial refused" || got.Adapters[1].Restarts != 5 {
		t.Fatalf("adapters[1] = %+v, want backoff/dial refused/restarts=5", got.Adapters[1])
	}
}

// TestCmdHealthHumanOutput proves the plain-text rendering lists every
// adapter with its channel/account/state/restarts.
func TestCmdHealthHumanOutput(t *testing.T) {
	backend := newFakeBackend()
	backend.health = []core.AdapterHealth{
		{Channel: core.ChannelMail, Account: "cl", State: core.AdapterConnected, Restarts: 0},
	}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"health"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "mail/cl") || !strings.Contains(out, "connected") {
		t.Fatalf("stdout = %q, want it to mention mail/cl connected", out)
	}
}

// TestCmdHealthPropagatesBackendError proves a backend error (e.g. the
// daemon unreachable) surfaces as a failing exit code, matching every
// other command's error convention.
func TestCmdHealthPropagatesBackendError(t *testing.T) {
	backend := newFakeBackend()
	backend.healthErr = context.DeadlineExceeded

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"health"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero on backend error", code)
	}
}
