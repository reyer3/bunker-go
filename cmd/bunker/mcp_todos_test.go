package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// todoSession serves the MCP tools over a real core.Service and store,
// so idempotency and the fill from the source message are the daemon's.
func todoSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Upsert(context.Background(), core.Item{
		ID: "whatsapp:personal:A1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "demo-ana",
		ThreadName: "Demo Ana", From: core.Address{ID: "demo-ana", Name: "Demo Ana"}, Body: "¿Me mandas el informe el viernes?",
	}); err != nil {
		t.Fatal(err)
	}
	// Not started with --allow-send: to-dos are local, so they work anyway.
	return mcpSession(t, core.NewService(st, core.NewRegistry()), false)
}

func TestMCPTodoToolsAnnotations(t *testing.T) {
	s := todoSession(t)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		have[tool.Name] = tool
	}
	for _, name := range []string{"todo_add", "todo_list", "todo_done"} {
		if have[name] == nil {
			t.Fatalf("tool %q missing", name)
		}
	}
	if !have["todo_list"].Annotations.ReadOnlyHint {
		t.Error("todo_list only reads")
	}
	for _, name := range []string{"todo_add", "todo_done"} {
		a := have[name].Annotations
		if a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint || !a.IdempotentHint {
			t.Errorf("%s annotations = %+v, want a local, idempotent, non-destructive write", name, a)
		}
	}
	if !strings.Contains(have["todo_add"].Description, "item_id") {
		t.Error("todo_add must tell the agent to pass item_id")
	}
	if !strings.Contains(mcpInstructions, "todo_add") || !strings.Contains(mcpInstructions, "todo_list") {
		t.Error("the server instructions must mention the to-do tools")
	}
}

func TestMCPTodoAddListDone(t *testing.T) {
	s := todoSession(t)
	args := map[string]any{"text": "Mandar el informe", "direction": "mine", "due": "2026-10-09", "item_id": "whatsapp:personal:A1"}
	res, out := callTool(t, s, "todo_add", args)
	if res.IsError {
		t.Fatalf("todo_add: %s", toolText(res))
	}
	todo, _ := out["todo"].(map[string]any)
	id, _ := todo["id"].(string)
	if id == "" || todo["person"] != "Demo Ana" || todo["channel"] != "whatsapp" || todo["due"] != "2026-10-09" || todo["status"] != "open" {
		t.Fatalf("todo_add = %v", out)
	}
	// A retry (or a rescan of the same message) never duplicates.
	if _, again := callTool(t, s, "todo_add", args); again["todo"].(map[string]any)["id"] != id {
		t.Fatalf("retry = %v, want %s", again, id)
	}
	if res, _ := callTool(t, s, "todo_add", map[string]any{"text": "Confirmar la sala", "direction": "theirs", "person": "Demo Luis"}); res.IsError {
		t.Fatalf("theirs: %s", toolText(res))
	}

	_, out = callTool(t, s, "todo_list", map[string]any{})
	if list, _ := out["todos"].([]any); len(list) != 2 {
		t.Fatalf("todo_list = %v, want 2 open", out)
	}
	_, out = callTool(t, s, "todo_list", map[string]any{"direction": "theirs"})
	if list, _ := out["todos"].([]any); len(list) != 1 {
		t.Fatalf("todo_list theirs = %v", out)
	}

	res, out = callTool(t, s, "todo_done", map[string]any{"id": id})
	if res.IsError || out["todo"].(map[string]any)["status"] != "done" || out["todo"].(map[string]any)["done"] == "" {
		t.Fatalf("todo_done = %v %s", out, toolText(res))
	}
	_, out = callTool(t, s, "todo_list", map[string]any{})
	if list, _ := out["todos"].([]any); len(list) != 1 {
		t.Fatalf("open after done = %v", out)
	}
	_, out = callTool(t, s, "todo_list", map[string]any{"status": "all"})
	if list, _ := out["todos"].([]any); len(list) != 2 {
		t.Fatalf("all = %v", out)
	}
	res, out = callTool(t, s, "todo_done", map[string]any{"id": id, "reopen": true})
	if res.IsError || out["todo"].(map[string]any)["status"] != "open" {
		t.Fatalf("reopen = %v %s", out, toolText(res))
	}
}

func TestMCPTodoErrors(t *testing.T) {
	s := todoSession(t)
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		want       string
	}{
		{"no text", "todo_add", map[string]any{"text": " ", "direction": "mine"}, "empty"},
		{"bad direction", "todo_add", map[string]any{"text": "x", "direction": "ours"}, "direction"},
		{"bad due", "todo_add", map[string]any{"text": "x", "direction": "mine", "due": "el viernes"}, "due"},
		{"unknown item", "todo_add", map[string]any{"text": "x", "direction": "mine", "item_id": "mail:cl:404"}, "not found"},
		{"bad status", "todo_list", map[string]any{"status": "pending"}, "status"},
		{"unknown id", "todo_done", map[string]any{"id": "nope"}, "not found"},
		{"no id", "todo_done", map[string]any{"id": ""}, "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := callTool(t, s, tc.tool, tc.args)
			if !res.IsError || !strings.Contains(toolText(res), tc.want) {
				t.Errorf("%s(%v) = %v %q, want an error about %q", tc.tool, tc.args, res.IsError, toolText(res), tc.want)
			}
		})
	}
}
