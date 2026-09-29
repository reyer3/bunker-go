package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
)

// mcpSession connects an in-memory MCP client to a server over backend.
func mcpSession(t *testing.T, backend Backend, allowSend bool) *mcp.ClientSession {
	t.Helper()
	dial := func(context.Context) (Backend, io.Closer, error) { return backend, nil, nil }
	server := newMCPServer(dial, allowSend)
	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return res, out
}

func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func mcpBackend() *fakeBackend {
	backend := contactsBackend()
	backend.items = map[string]core.Item{
		"whatsapp:personal:1": {
			ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "51911@s.whatsapp.net",
			From: core.Address{ID: "51911@s.whatsapp.net", Name: "José Pérez"}, Body: strings.Repeat("hola ", 100), Unread: true,
			Timestamp: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC),
		},
	}
	backend.counts = map[core.Channel]map[string]int{core.ChannelWhatsApp: {"personal": 1}}
	return backend
}

func TestMCPListsTools(t *testing.T) {
	s := mcpSession(t, mcpBackend(), false)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		have[tool.Name] = tool
	}
	for _, name := range []string{"counts", "list", "search", "search_remote", "backfill", "read", "thread", "contacts", "calls", "health", "send", "reply"} {
		if have[name] == nil {
			t.Fatalf("tool %q missing", name)
		}
	}
	if !have["read"].Annotations.ReadOnlyHint || !have["health"].Annotations.ReadOnlyHint || have["send"].Annotations.ReadOnlyHint {
		t.Error("reads must be marked read-only and sends must not")
	}
	if !have["search"].Annotations.ReadOnlyHint || !have["list"].Annotations.ReadOnlyHint {
		t.Error("search and list only read the store")
	}
	// The server-side tools write what they find into the store and
	// reach the network: not read-only, but not destructive either.
	for _, name := range []string{"search_remote", "backfill"} {
		a := have[name].Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || !*a.OpenWorldHint {
			t.Errorf("%s annotations = %+v", name, a)
		}
	}
}

// mcpPagedBackend holds three items sharing a timestamp, so paging goes
// through the id tiebreak.
func mcpPagedBackend() *fakeBackend {
	backend := mcpBackend()
	ts := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"mail:work:a", "mail:work:b", "mail:work:c"} {
		backend.items[id] = core.Item{ID: id, Channel: core.ChannelMail, Account: "work", Subject: "factura", Body: strings.Repeat("x", 500), Timestamp: ts}
	}
	return backend
}

func TestMCPSearchPagesThroughTheStore(t *testing.T) {
	backend := mcpPagedBackend()
	s := mcpSession(t, backend, false)

	res, out := callTool(t, s, "search", map[string]any{"query": `from:ana "orden de compra" -is:read`, "limit": 2})
	items, _ := out["items"].([]any)
	cursor, _ := out["next_cursor"].(string)
	if res.IsError || len(items) != 2 || cursor == "" {
		t.Fatalf("search page 1 = %v (%s)", out, toolText(res))
	}
	call := backend.listPageCalls[0]
	if call.Query != `from:ana "orden de compra" -is:read` || call.Filter.Limit != 2 || call.Filter.Cursor != "" {
		t.Fatalf("backend got %+v", call)
	}
	if body := items[0].(map[string]any)["body"].(string); len([]rune(body)) > mcpSnippetLimit+1 {
		t.Errorf("search should return snippets like list, got %d runes", len([]rune(body)))
	}

	res, out = callTool(t, s, "search", map[string]any{"query": "factura", "limit": 2, "cursor": cursor})
	items, _ = out["items"].([]any)
	if res.IsError || len(items) != 2 || out["next_cursor"] != "" || backend.listPageCalls[1].Filter.Cursor != cursor {
		t.Fatalf("search page 2 = %v (%s)", out, toolText(res))
	}
	if len(backend.readCalls) != 0 {
		t.Fatalf("search must never read (and so never mark read): %+v", backend.readCalls)
	}

	res, _ = callTool(t, s, "search", map[string]any{"query": "  "})
	if !res.IsError || !strings.Contains(toolText(res), "query is required") {
		t.Fatalf("an empty query should be a tool error: %s", toolText(res))
	}
	backend.listErr = errors.New(`core: query: unknown operator "foo:"`)
	res, _ = callTool(t, s, "search", map[string]any{"query": "foo:bar"})
	if !res.IsError || !strings.Contains(toolText(res), `"foo:"`) {
		t.Fatalf("a query error should reach the agent: %s", toolText(res))
	}
}

func TestMCPListPaginatesAndStaysCompatible(t *testing.T) {
	backend := mcpPagedBackend()
	s := mcpSession(t, backend, false)

	// Without a cursor, list still answers as before, with next_cursor
	// added.
	_, out := callTool(t, s, "list", map[string]any{"channel": "mail", "limit": 2})
	items, _ := out["items"].([]any)
	cursor, _ := out["next_cursor"].(string)
	if len(items) != 2 || cursor == "" {
		t.Fatalf("list page 1 = %v", out)
	}
	call := backend.listPageCalls[0]
	if call.Query != "" || call.Filter.Channel != core.ChannelMail || call.Filter.Limit != 2 {
		t.Fatalf("backend got %+v", call)
	}
	_, out = callTool(t, s, "list", map[string]any{"channel": "mail", "limit": 2, "cursor": cursor})
	if items, _ := out["items"].([]any); len(items) != 1 || out["next_cursor"] != "" {
		t.Fatalf("list page 2 = %v", out)
	}
	_, out = callTool(t, s, "list", nil)
	if items, _ := out["items"].([]any); len(items) != 4 || backend.listPageCalls[2].Filter.Limit != mcpListDefault {
		t.Fatalf("default list = %v, call %+v", out, backend.listPageCalls[2])
	}
}

func TestMCPSearchRemote(t *testing.T) {
	backend := mcpBackend()
	backend.searchItems = []core.Item{{ID: "mail:work:9", Channel: core.ChannelMail, Account: "work", Subject: "viejo", Body: strings.Repeat("y", 500)}}
	s := mcpSession(t, backend, false)

	res, out := callTool(t, s, "search_remote", map[string]any{"account": "work", "from": "ana", "since": "2025-01-31", "before": "2w"})
	items, _ := out["items"].([]any)
	if res.IsError || len(items) != 1 {
		t.Fatalf("search_remote = %v (%s)", out, toolText(res))
	}
	if len(backend.searchCalls) != 1 {
		t.Fatalf("calls = %+v", backend.searchCalls)
	}
	c := backend.searchCalls[0]
	wantSince := time.Date(2025, 1, 31, 0, 0, 0, 0, time.Local)
	if c.Channel != core.ChannelMail || c.Account != "work" || c.Criteria.From != "ana" || c.Criteria.Folder != "INBOX" ||
		c.Criteria.Limit != mcpListDefault || !c.Criteria.Since.Equal(wantSince) {
		t.Fatalf("criteria = %+v", c)
	}
	if ago := time.Since(c.Criteria.Before); ago < 13*24*time.Hour || ago > 15*24*time.Hour {
		t.Errorf("before 2w = %v, want about two weeks ago", c.Criteria.Before)
	}
	if body := items[0].(map[string]any)["body"].(string); len([]rune(body)) > mcpSnippetLimit+1 {
		t.Errorf("search_remote should return snippets, got %d runes", len([]rune(body)))
	}

	res, _ = callTool(t, s, "search_remote", map[string]any{"account": "work", "since": "last week"})
	if !res.IsError || !strings.Contains(toolText(res), "since") || len(backend.searchCalls) != 1 {
		t.Fatalf("a bad date must fail before any server search: %s", toolText(res))
	}
}

func TestMCPBackfill(t *testing.T) {
	backend := mcpBackend()
	backend.backfillResult = core.BackfillResult{Count: 2, FirstID: "mail:work:1", LastID: "mail:work:2"}
	s := mcpSession(t, backend, false)

	res, out := callTool(t, s, "backfill", map[string]any{"account": "work", "since": "30d", "dry_run": true})
	if res.IsError || out["count"] != float64(2) || out["dry_run"] != true || out["first_id"] != "mail:work:1" {
		t.Fatalf("backfill = %v (%s)", out, toolText(res))
	}
	c := backend.backfillCalls[0]
	if c.Channel != core.ChannelMail || c.Account != "work" || c.Folder != "INBOX" || !c.DryRun {
		t.Fatalf("backfill call = %+v", c)
	}
	if ago := time.Since(c.Since); ago < 29*24*time.Hour || ago > 31*24*time.Hour {
		t.Errorf("since 30d = %v", c.Since)
	}

	callTool(t, s, "backfill", map[string]any{"account": "work", "since": "2026-01-01", "folder": "Archive"})
	if c := backend.backfillCalls[1]; c.Folder != "Archive" || c.DryRun || !c.Since.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("backfill call = %+v", c)
	}

	// The schema marks since required; an empty one gets the handler's
	// own error. Neither reaches the server.
	res, _ = callTool(t, s, "backfill", map[string]any{"account": "work"})
	if !res.IsError || !strings.Contains(toolText(res), "since") || len(backend.backfillCalls) != 2 {
		t.Fatalf("backfill without since must fail before the server: %s", toolText(res))
	}
	res, _ = callTool(t, s, "backfill", map[string]any{"account": "work", "since": ""})
	if !res.IsError || !strings.Contains(toolText(res), "since is required") || len(backend.backfillCalls) != 2 {
		t.Fatalf("backfill without since must fail before the server: %s", toolText(res))
	}
	backend.backfillErr = core.ErrUnsupported
	res, _ = callTool(t, s, "backfill", map[string]any{"account": "personal", "since": "1w"})
	if !res.IsError {
		t.Fatal("a backend error must be a tool error")
	}
}

func TestMCPReadTools(t *testing.T) {
	backend := mcpBackend()
	s := mcpSession(t, backend, false)

	_, counts := callTool(t, s, "counts", nil)
	if !strings.Contains(mustJSON(t, counts), `"personal":1`) {
		t.Fatalf("counts = %v", counts)
	}
	res, list := callTool(t, s, "list", map[string]any{"unread": true})
	items, _ := list["items"].([]any)
	if res.IsError || len(items) != 1 {
		t.Fatalf("list = %v (%s)", list, toolText(res))
	}
	if body := items[0].(map[string]any)["body"].(string); len([]rune(body)) > mcpSnippetLimit+1 {
		t.Errorf("list should return a snippet, got %d runes", len([]rune(body)))
	}
	_, item := callTool(t, s, "read", map[string]any{"id": "whatsapp:personal:1"})
	if item["from"] != "José Pérez <51911@s.whatsapp.net>" || !strings.HasPrefix(item["body"].(string), "hola hola") {
		t.Fatalf("read = %v", item)
	}
	if len(backend.readCalls) != 1 || backend.readCalls[0].MarkReceipt {
		t.Fatalf("read must never mark read: %+v", backend.readCalls)
	}
	_, contacts := callTool(t, s, "contacts", map[string]any{"query": "ana"})
	if list, _ := contacts["contacts"].([]any); len(list) == 0 {
		t.Fatalf("contacts = %v", contacts)
	}
}

func TestMCPSendPlansUnlessAllowed(t *testing.T) {
	backend := mcpBackend()
	s := mcpSession(t, backend, false)

	res, out := callTool(t, s, "send", map[string]any{"channel": "whatsapp", "account": "personal", "to": "jose", "text": "hola"})
	if res.IsError || out["sent"] != false || len(backend.sendCalls) != 1 || !backend.sendDryRuns[0] {
		t.Fatalf("default send: %v %s, calls %+v", out, toolText(res), backend.sendCalls)
	}
	if backend.sendCalls[0].To[0] != "51911@s.whatsapp.net" {
		t.Fatalf("the name should resolve like the CLI: %+v", backend.sendCalls[0])
	}

	res, _ = callTool(t, s, "send", map[string]any{"channel": "whatsapp", "account": "personal", "to": "jose", "text": "hola", "confirm": true})
	if !res.IsError || !strings.Contains(toolText(res), "--allow-send") {
		t.Fatalf("confirm without --allow-send should explain why nothing was sent: %s", toolText(res))
	}
	for _, dry := range backend.sendDryRuns {
		if !dry {
			t.Fatal("a plans-only server sent for real")
		}
	}

	res, _ = callTool(t, s, "send", map[string]any{"channel": "whatsapp", "account": "personal", "to": "ana", "text": "hola"})
	if !res.IsError || !strings.Contains(toolText(res), "Ana Díaz") {
		t.Fatalf("an ambiguous name should fail with the candidates: %s", toolText(res))
	}
}

func TestMCPSendWhenAllowed(t *testing.T) {
	backend := mcpBackend()
	backend.receipt = core.Receipt{ID: "R1", Channel: core.ChannelWhatsApp}
	s := mcpSession(t, backend, true)

	res, out := callTool(t, s, "reply", map[string]any{"id": "whatsapp:personal:1", "text": "listo", "confirm": true})
	if res.IsError || out["sent"] != true {
		t.Fatalf("allowed confirm: %v %s", out, toolText(res))
	}
	if len(backend.replyCalls) != 2 || !backend.replyCalls[0].DryRun || backend.replyCalls[1].DryRun {
		t.Fatalf("a real send is always planned first: %+v", backend.replyCalls)
	}

	// Without confirm, even an allowed server only plans.
	callTool(t, s, "reply", map[string]any{"id": "whatsapp:personal:1", "text": "listo"})
	if len(backend.replyCalls) != 3 || !backend.replyCalls[2].DryRun {
		t.Fatalf("no confirm should plan only: %+v", backend.replyCalls)
	}
}

func TestMCPDaemonDown(t *testing.T) {
	dial := func(context.Context) (Backend, io.Closer, error) {
		return nil, nil, errors.New("cannot reach bunker daemon")
	}
	server := newMCPServer(dial, false)
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, _ := callTool(t, s, "counts", nil)
	if !res.IsError || !strings.Contains(toolText(res), "daemon") {
		t.Fatalf("a dead daemon should be a tool error: %s", toolText(res))
	}
}

func TestMCPHealth(t *testing.T) {
	backend := mcpBackend()
	since := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	backend.health = []core.AdapterHealth{
		{Channel: core.ChannelWhatsApp, Account: "personal", State: core.AdapterConnected, Since: since},
		{Channel: core.ChannelMail, Account: "work", State: core.AdapterBackoff, Since: since, LastError: "imap: login failed", Restarts: 3},
	}
	s := mcpSession(t, backend, false)

	res, out := callTool(t, s, "health", nil)
	if res.IsError || out["daemon_up"] != true {
		t.Fatalf("health = %v (%s)", out, toolText(res))
	}
	adapters, _ := out["adapters"].([]any)
	if len(adapters) != 2 {
		t.Fatalf("adapters = %v", out["adapters"])
	}
	wa, mail := adapters[0].(map[string]any), adapters[1].(map[string]any)
	if wa["state"] != "connected" || wa["since"] != "2026-09-28T09:00:00Z" || wa["last_item"] != "2026-09-28T10:00:00Z" {
		t.Errorf("whatsapp = %v", wa)
	}
	if mail["state"] != "backoff" || mail["last_error"] != "imap: login failed" || mail["restarts"] != float64(3) {
		t.Errorf("mail = %v", mail)
	}
	if _, ok := mail["last_item"]; ok {
		t.Errorf("an account with no items has no last_item: %v", mail)
	}
}

func TestMCPHealthDaemonDown(t *testing.T) {
	dial := func(context.Context) (Backend, io.Closer, error) {
		return nil, nil, errors.New("cannot reach bunker daemon")
	}
	server := newMCPServer(dial, false)
	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverT, nil); err != nil {
		t.Fatal(err)
	}
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res, out := callTool(t, s, "health", nil)
	if res.IsError || out["daemon_up"] != false {
		t.Fatalf("a dead daemon is a successful health answer: %v (%s)", out, toolText(res))
	}
	if hint, _ := out["hint"].(string); !strings.Contains(hint, "bunker daemon") {
		t.Errorf("hint = %q", hint)
	}
	if !strings.Contains(out["error"].(string), "cannot reach") {
		t.Errorf("error = %v", out["error"])
	}
}

func TestMCPHealthBackendError(t *testing.T) {
	backend := mcpBackend()
	backend.healthErr = errors.New("rpc: unknown method")
	s := mcpSession(t, backend, false)
	res, _ := callTool(t, s, "health", nil)
	if !res.IsError || !strings.Contains(toolText(res), "unknown method") {
		t.Fatalf("a reachable daemon that fails health is a tool error: %s", toolText(res))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
