package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

const mcpMailID = "mail:work:INBOX:1:7"

// organizeBackend is mcpBackend plus a mail item, so both a chat and a
// mail item can be organized.
func organizeBackend() *fakeBackend {
	backend := mcpBackend()
	backend.items[mcpMailID] = core.Item{
		ID: mcpMailID, Channel: core.ChannelMail, Account: "work", From: core.Address{ID: "someone@example.org"},
		Subject: "factura", Unread: true, Timestamp: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC),
	}
	return backend
}

func realOrganizeCalls(b *fakeBackend) []organizeCall {
	var out []organizeCall
	for _, c := range b.organizeCalls {
		if !c.DryRun {
			out = append(out, c)
		}
	}
	return out
}

func TestMCPOrganizeToolsListed(t *testing.T) {
	s := mcpSession(t, organizeBackend(), false)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	organizeTools := map[string]bool{"mark_read": true, "mark_unread": true, "archive": true, "move": true, "label": true}
	for _, tool := range tools.Tools {
		have[tool.Name] = true
		if organizeTools[tool.Name] && tool.Annotations.ReadOnlyHint {
			t.Errorf("%s changes things and must not be read-only", tool.Name)
		}
	}
	for _, name := range []string{"mark_read", "mark_unread", "archive", "move", "label"} {
		if !have[name] {
			t.Errorf("tool %q missing", name)
		}
	}
}

func TestMCPOrganizePlansByDefault(t *testing.T) {
	backend := organizeBackend()
	s := mcpSession(t, backend, true)

	res, out := callTool(t, s, "mark_read", map[string]any{"id": "whatsapp:personal:1"})
	if res.IsError || out["done"] != false {
		t.Fatalf("mark_read without confirm: %v %s", out, toolText(res))
	}
	plan := out["plan"].(map[string]any)
	if plan["channel"] != "whatsapp" || plan["notifies_sender"] != true || !strings.Contains(plan["change"].(string), "read receipt") {
		t.Fatalf("a WhatsApp mark_read plan must say it notifies the sender: %v", plan)
	}

	_, out = callTool(t, s, "mark_read", map[string]any{"id": mcpMailID})
	if plan := out["plan"].(map[string]any); plan["notifies_sender"] != false {
		t.Fatalf("mail mark_read notifies nobody: %v", plan)
	}

	_, out = callTool(t, s, "mark_unread", map[string]any{"id": "whatsapp:personal:1"})
	if plan := out["plan"].(map[string]any); plan["local_only"] != true || plan["notifies_sender"] != false {
		t.Fatalf("WhatsApp mark_unread is local only: %v", plan)
	}

	callTool(t, s, "archive", map[string]any{"id": mcpMailID})
	callTool(t, s, "move", map[string]any{"id": mcpMailID, "folder": "Junk"})
	callTool(t, s, "label", map[string]any{"id": mcpMailID, "add": []string{"pagar"}})

	if got := realOrganizeCalls(backend); len(got) != 0 {
		t.Fatalf("plans must never organize for real: %+v", got)
	}
	if len(backend.unreadCalls) != 0 {
		t.Fatalf("plans must never mark unread: %v", backend.unreadCalls)
	}
}

func TestMCPOrganizeConfirmNeedsAllowSend(t *testing.T) {
	backend := organizeBackend()
	s := mcpSession(t, backend, false)

	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"mark_read", map[string]any{"id": "whatsapp:personal:1", "confirm": true}},
		{"mark_unread", map[string]any{"id": mcpMailID, "confirm": true}},
		{"archive", map[string]any{"id": mcpMailID, "confirm": true}},
	} {
		res, out := callTool(t, s, call.tool, call.args)
		if !res.IsError || !strings.Contains(toolText(res), "--allow-send") || out["done"] != false {
			t.Fatalf("%s confirm on a plans-only server: %v %s", call.tool, out, toolText(res))
		}
		if !strings.Contains(toolText(res), `"action":"`+call.tool+`"`) {
			t.Errorf("%s should show what would change: %s", call.tool, toolText(res))
		}
	}
	if got := realOrganizeCalls(backend); len(got) != 0 || len(backend.unreadCalls) != 0 {
		t.Fatalf("nothing may change without --allow-send: organize %+v, unread %v", got, backend.unreadCalls)
	}
}

func TestMCPOrganizeExecutesWhenAllowed(t *testing.T) {
	backend := organizeBackend()
	backend.unreadLocal = true
	s := mcpSession(t, backend, true)

	res, out := callTool(t, s, "mark_read", map[string]any{"id": "whatsapp:personal:1", "confirm": true})
	if res.IsError || out["done"] != true {
		t.Fatalf("mark_read: %v %s", out, toolText(res))
	}
	calls := realOrganizeCalls(backend)
	if len(calls) != 1 || calls[0].ID != "whatsapp:personal:1" || calls[0].Op.Seen == nil || !*calls[0].Op.Seen {
		t.Fatalf("mark_read should organize Seen=true: %+v", calls)
	}
	if !backend.organizeCalls[0].DryRun {
		t.Fatal("a real change is always planned first")
	}

	res, out = callTool(t, s, "mark_unread", map[string]any{"id": mcpMailID, "confirm": true})
	if res.IsError || out["done"] != true || out["plan"].(map[string]any)["local_only"] != true {
		t.Fatalf("mark_unread: %v %s", out, toolText(res))
	}
	if len(backend.unreadCalls) != 1 || backend.unreadCalls[0] != mcpMailID {
		t.Fatalf("mark_unread should go through MarkUnread: %v", backend.unreadCalls)
	}

	callTool(t, s, "archive", map[string]any{"id": mcpMailID, "confirm": true})
	callTool(t, s, "move", map[string]any{"id": mcpMailID, "folder": "Junk", "confirm": true})
	callTool(t, s, "label", map[string]any{"id": mcpMailID, "add": []string{"pagar"}, "remove": []string{"nuevo"}, "confirm": true})
	calls = realOrganizeCalls(backend)
	if len(calls) != 4 {
		t.Fatalf("organize calls = %+v", calls)
	}
	if calls[1].Op.MoveTo != mcpArchiveFolder || calls[2].Op.MoveTo != "Junk" {
		t.Errorf("archive/move ops = %+v, %+v", calls[1].Op, calls[2].Op)
	}
	if strings.Join(calls[3].Op.AddLabels, ",") != "pagar" || strings.Join(calls[3].Op.RemoveLabels, ",") != "nuevo" {
		t.Errorf("label op = %+v", calls[3].Op)
	}
}

func TestMCPOrganizeErrorsSurface(t *testing.T) {
	backend := organizeBackend()
	s := mcpSession(t, backend, true)

	res, _ := callTool(t, s, "archive", map[string]any{"id": "whatsapp:personal:1", "confirm": true})
	if !res.IsError || !strings.Contains(toolText(res), "no folders or labels") {
		t.Fatalf("archiving a chat must fail loudly: %s", toolText(res))
	}
	res, _ = callTool(t, s, "mark_read", map[string]any{"id": "mail:work:INBOX:1:404"})
	if !res.IsError || !strings.Contains(toolText(res), "not found") {
		t.Fatalf("an unknown id must fail: %s", toolText(res))
	}
	res, _ = callTool(t, s, "move", map[string]any{"id": mcpMailID, "folder": ""})
	if !res.IsError || !strings.Contains(toolText(res), "folder is required") {
		t.Fatalf("move without a folder must fail: %s", toolText(res))
	}
	res, _ = callTool(t, s, "label", map[string]any{"id": mcpMailID})
	if !res.IsError || !strings.Contains(toolText(res), "at least one label") {
		t.Fatalf("label with nothing to do must fail: %s", toolText(res))
	}
	if got := realOrganizeCalls(backend); len(got) != 0 {
		t.Fatalf("a failed plan must not organize: %+v", got)
	}

	backend.organizeErr = errors.New("mail: organize: select INBOX: connection reset")
	res, out := callTool(t, s, "archive", map[string]any{"id": mcpMailID, "confirm": true})
	if !res.IsError || !strings.Contains(toolText(res), "connection reset") || out["done"] == true {
		t.Fatalf("a backend error must be a tool error: %v %s", out, toolText(res))
	}
	backend.organizeErr = nil
	backend.unreadErr = errors.New("store: locked")
	res, _ = callTool(t, s, "mark_unread", map[string]any{"id": mcpMailID, "confirm": true})
	if !res.IsError || !strings.Contains(toolText(res), "store: locked") {
		t.Fatalf("a MarkUnread error must be a tool error: %s", toolText(res))
	}
}
