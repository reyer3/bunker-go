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
	for _, name := range []string{"counts", "list", "read", "thread", "contacts", "calls", "send", "reply"} {
		if have[name] == nil {
			t.Errorf("tool %q missing", name)
		}
	}
	if !have["read"].Annotations.ReadOnlyHint || have["send"].Annotations.ReadOnlyHint {
		t.Error("reads must be marked read-only and sends must not")
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

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
