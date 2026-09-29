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

func pagedBackend() *fakeBackend {
	b := newFakeBackend()
	ts := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for _, id := range []string{"mail:cl:a", "mail:cl:b", "mail:cl:c"} {
		b.items[id] = core.Item{ID: id, Channel: core.ChannelMail, Account: "cl", Subject: "s-" + id, Timestamp: ts}
	}
	return b
}

func TestCmdListQueryAndCursorReachTheBackend(t *testing.T) {
	b := pagedBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), b, []string{"list", "--query", "from:ana -is:read", "--limit", "2", "--channel", "mail", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if len(b.listPageCalls) != 1 {
		t.Fatalf("calls = %+v", b.listPageCalls)
	}
	call := b.listPageCalls[0]
	if call.Query != "from:ana -is:read" || call.Filter.Limit != 2 || call.Filter.Channel != core.ChannelMail || call.Filter.Cursor != "" {
		t.Fatalf("call = %+v", call)
	}
	var got struct {
		Items      []core.Item `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.NextCursor == nil || *got.NextCursor == "" {
		t.Fatalf("page 1 = %s", stdout.String())
	}

	stdout.Reset()
	code = runWithBackend(context.Background(), b, []string{"list", "--limit", "2", "--cursor", *got.NextCursor, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || b.listPageCalls[1].Filter.Cursor != *got.NextCursor {
		t.Fatalf("exit %d, call %+v", code, b.listPageCalls[1])
	}
	var last struct {
		Items      []core.Item `json:"items"`
		NextCursor *string     `json:"next_cursor"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &last); err != nil {
		t.Fatal(err)
	}
	// The last page still carries the key, empty, so a script can loop
	// on it without checking for its presence.
	if len(last.Items) != 1 || last.NextCursor == nil || *last.NextCursor != "" {
		t.Fatalf("page 2 = %s", stdout.String())
	}
}

func TestCmdListTextPrintsCursorOnStderr(t *testing.T) {
	b := pagedBackend()
	var stdout, stderr bytes.Buffer
	if code := runWithBackend(context.Background(), b, []string{"list", "--limit", "1"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if lines := strings.Split(strings.TrimSpace(stdout.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "mail:cl:c") {
		t.Fatalf("stdout = %q, want exactly one item line", stdout.String())
	}
	if !strings.HasPrefix(stderr.String(), "next_cursor: ") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCmdFindJoinsPositionalsAndDefaultsTheLimit(t *testing.T) {
	b := pagedBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), b, []string{"find", "from:ana", "--json", `"orden de compra"`, "--unread"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	call := b.listPageCalls[0]
	if call.Query != `from:ana "orden de compra"` || call.Filter.Limit != defaultFindLimit || call.Filter.Unread == nil || !*call.Filter.Unread {
		t.Fatalf("call = %+v", call)
	}
	if !strings.Contains(stdout.String(), `"next_cursor"`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestCmdFindNeedsAQuery(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithBackend(context.Background(), newFakeBackend(), []string{"find"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: bunker find") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCmdFindBackendErrorIsJSON(t *testing.T) {
	b := newFakeBackend()
	b.listErr = errTest
	var stdout, stderr bytes.Buffer
	if code := runWithBackend(context.Background(), b, []string{"find", "x", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(stdout.String(), `"error"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}
