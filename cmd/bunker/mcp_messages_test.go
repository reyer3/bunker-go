package main

import (
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestMCPMessageToolsPlanUnlessAllowed(t *testing.T) {
	backend := mcpBackend()
	s := mcpSession(t, backend, false)

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"edit", map[string]any{"id": ownID, "text": "corregido"}},
		{"delete", map[string]any{"id": ownID}},
		{"react", map[string]any{"id": "whatsapp:personal:1", "emoji": "👍"}},
	}
	for _, c := range calls {
		res, out := callTool(t, s, c.tool, c.args)
		if res.IsError || out["sent"] != false {
			t.Fatalf("%s without confirm: %v %s", c.tool, out, toolText(res))
		}
		confirmed := map[string]any{"confirm": true}
		for k, v := range c.args {
			confirmed[k] = v
		}
		res, _ = callTool(t, s, c.tool, confirmed)
		if !res.IsError || !strings.Contains(toolText(res), "--allow-send") {
			t.Fatalf("%s confirm on a plans-only server: %s", c.tool, toolText(res))
		}
	}
	for _, call := range backend.actionCalls {
		if !call.DryRun {
			t.Fatalf("a plans-only server changed a message: %+v", backend.actionCalls)
		}
	}
}

func TestMCPMessageToolsWhenAllowed(t *testing.T) {
	backend := mcpBackend()
	backend.receipt = core.Receipt{ID: "R1", Channel: core.ChannelWhatsApp}
	s := mcpSession(t, backend, true)

	res, out := callTool(t, s, "react", map[string]any{"id": "whatsapp:personal:1", "emoji": "", "confirm": true})
	if res.IsError || out["sent"] != true {
		t.Fatalf("allowed react: %v %s", out, toolText(res))
	}
	if len(backend.actionCalls) != 2 || !backend.actionCalls[0].DryRun || backend.actionCalls[1].DryRun {
		t.Fatalf("a real react is planned first: %+v", backend.actionCalls)
	}
	if real := backend.actionCalls[1]; real.Text != "" || !strings.HasPrefix(real.Key, "mcp-") {
		t.Errorf("real call = %+v, want the removal with a plan-derived idempotency key", real)
	}

	callTool(t, s, "delete", map[string]any{"id": ownID})
	if last := backend.actionCalls[len(backend.actionCalls)-1]; !last.DryRun {
		t.Fatal("delete without confirm deleted")
	}
}
