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

func TestCmdBackfillBuildsCallAndPrintsResult(t *testing.T) {
	backend := newFakeBackend()
	backend.backfillResult = core.BackfillResult{Count: 2, FirstID: "mail:cl:1.1", LastID: "mail:cl:1.2"}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "mail", "cl", "--since", "2026-01-01"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.backfillCalls) != 1 {
		t.Fatalf("backfillCalls = %+v, want 1 call", backend.backfillCalls)
	}
	call := backend.backfillCalls[0]
	wantSince := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if call.Channel != core.ChannelMail || call.Account != "cl" || call.Folder != "INBOX" || call.DryRun {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if !call.Since.Equal(wantSince) {
		t.Fatalf("Since = %v, want %v", call.Since, wantSince)
	}
	if !strings.Contains(stdout.String(), "mail:cl:1.1") || !strings.Contains(stdout.String(), "mail:cl:1.2") {
		t.Fatalf("stdout = %q, want it to mention the id range", stdout.String())
	}
}

func TestCmdBackfillCustomFolderAndDryRun(t *testing.T) {
	backend := newFakeBackend()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{
		"backfill", "mail", "cl", "--since", "2026-01-01", "--folder", "Archive", "--dry-run",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	call := backend.backfillCalls[0]
	if call.Folder != "Archive" || !call.DryRun {
		t.Fatalf("call = %+v, want Folder=Archive DryRun=true", call)
	}
}

func TestCmdBackfillRequiresSince(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "mail", "cl"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero: --since is required")
	}
	if len(backend.backfillCalls) != 0 {
		t.Fatalf("backfillCalls = %+v, want 0", backend.backfillCalls)
	}
}

func TestCmdBackfillRejectsMalformedSince(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "mail", "cl", "--since", "not-a-date"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero for a malformed --since")
	}
	if len(backend.backfillCalls) != 0 {
		t.Fatalf("backfillCalls = %+v, want 0", backend.backfillCalls)
	}
}

func TestCmdBackfillRejectsUnsupportedChannel(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "whatsapp", "personal", "--since", "2026-01-01"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for an unsupported channel", code)
	}
	if len(backend.backfillCalls) != 0 {
		t.Fatalf("backfillCalls = %+v, want 0", backend.backfillCalls)
	}
}

func TestCmdBackfillJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.backfillResult = core.BackfillResult{Count: 5}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "mail", "cl", "--since", "2026-01-01", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		DryRun bool                `json:"dryRun"`
		Result core.BackfillResult `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Result.Count != 5 {
		t.Fatalf("Result.Count = %d, want 5", got.Result.Count)
	}
}

func TestCmdBackfillErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.backfillErr = errTest

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"backfill", "mail", "cl", "--since", "2026-01-01", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit code on backend error")
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if got.Error == "" {
		t.Fatal("expected a non-empty error field")
	}
}
