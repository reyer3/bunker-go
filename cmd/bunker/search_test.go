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

func TestCmdSearchBuildsCallAndPrintsItems(t *testing.T) {
	backend := newFakeBackend()
	backend.searchItems = []core.Item{
		{ID: "mail:cl:1.1", Channel: core.ChannelMail, Account: "cl", Subject: "hi", Unread: true},
	}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"search", "mail", "cl", "--from", "alice@x", "--subject", "invoice"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if len(backend.searchCalls) != 1 {
		t.Fatalf("searchCalls = %+v, want 1 call", backend.searchCalls)
	}
	call := backend.searchCalls[0]
	if call.Channel != core.ChannelMail || call.Account != "cl" {
		t.Fatalf("call = %+v, unexpected", call)
	}
	if call.Criteria.From != "alice@x" || call.Criteria.Subject != "invoice" {
		t.Fatalf("Criteria = %+v, want From=alice@x Subject=invoice", call.Criteria)
	}
	if call.Criteria.Folder != "INBOX" {
		t.Fatalf("Criteria.Folder = %q, want the INBOX default", call.Criteria.Folder)
	}
	if call.Criteria.Limit != 50 {
		t.Fatalf("Criteria.Limit = %d, want the 50 default", call.Criteria.Limit)
	}
	if !strings.Contains(stdout.String(), "mail:cl:1.1") || !strings.Contains(stdout.String(), "hi") {
		t.Fatalf("stdout = %q, want it to mention the result like list does", stdout.String())
	}
}

func TestCmdSearchParsesSinceBeforeFolderAndLimit(t *testing.T) {
	backend := newFakeBackend()

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{
		"search", "mail", "cl", "--since", "2026-01-01", "--before", "2026-06-01", "--folder", "Archive", "--limit", "10",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	call := backend.searchCalls[0]
	wantSince := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wantBefore := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !call.Criteria.Since.Equal(wantSince) || !call.Criteria.Before.Equal(wantBefore) {
		t.Fatalf("Criteria Since/Before = %v/%v, want %v/%v", call.Criteria.Since, call.Criteria.Before, wantSince, wantBefore)
	}
	if call.Criteria.Folder != "Archive" || call.Criteria.Limit != 10 {
		t.Fatalf("Criteria = %+v, want Folder=Archive Limit=10", call.Criteria)
	}
}

func TestCmdSearchRejectsUnsupportedChannel(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"search", "whatsapp", "personal"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 for an unsupported channel", code)
	}
	if len(backend.searchCalls) != 0 {
		t.Fatalf("searchCalls = %+v, want 0", backend.searchCalls)
	}
}

func TestCmdSearchRequiresAccount(t *testing.T) {
	backend := newFakeBackend()
	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"search", "mail"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2 (missing account positional)", code)
	}
}

func TestCmdSearchJSON(t *testing.T) {
	backend := newFakeBackend()
	backend.searchItems = []core.Item{{ID: "mail:cl:1.1", Channel: core.ChannelMail}}

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"search", "mail", "cl", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	var got struct {
		Items []core.Item `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal stdout %q: %v", stdout.String(), err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "mail:cl:1.1" {
		t.Fatalf("Items = %+v", got.Items)
	}
}

func TestCmdSearchErrorProducesJSONError(t *testing.T) {
	backend := newFakeBackend()
	backend.searchErr = errTest

	var stdout, stderr bytes.Buffer
	code := runWithBackend(context.Background(), backend, []string{"search", "mail", "cl", "--json"}, strings.NewReader(""), &stdout, &stderr)
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
