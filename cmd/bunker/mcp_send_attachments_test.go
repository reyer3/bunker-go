package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// mediaAdapter is a WhatsApp adapter that accepts PNG attachments, so a
// real core.Service computes the plan's attachment list (name, MIME,
// size) exactly as the daemon does.
type mediaAdapter struct {
	sends atomic.Int32
	last  core.Outgoing
}

func (a *mediaAdapter) Channel() core.Channel                      { return core.ChannelWhatsApp }
func (a *mediaAdapter) Account() string                            { return "personal" }
func (a *mediaAdapter) Run(ctx context.Context, _ core.Sink) error { return nil }
func (a *mediaAdapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	return a.SendMedia(ctx, out)
}

func (a *mediaAdapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	a.sends.Add(1)
	a.last = out
	return core.Receipt{ID: "R1", Channel: core.ChannelWhatsApp, At: time.Now()}, nil
}

func (a *mediaAdapter) AttachmentPolicy() core.AttachmentPolicy {
	return core.AttachmentPolicy{MaxBytes: map[string]int64{"image/png": 1 << 20}}
}

func mediaServiceSession(t *testing.T, allowSend bool) (*mediaAdapter, func(name string, args map[string]any) (bool, map[string]any, string)) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	adapter := &mediaAdapter{}
	reg := core.NewRegistry()
	reg.Register(adapter)
	var backend Backend = daemonBackend{core.NewService(st, reg)}
	s := mcpSession(t, backend, allowSend)
	return adapter, func(name string, args map[string]any) (bool, map[string]any, string) {
		res, out := callTool(t, s, name, args)
		return res.IsError, out, toolText(res)
	}
}

func TestMCPSendPlanListsAttachments(t *testing.T) {
	_, call := mediaServiceSession(t, false)
	png := writeTempFile(t, "plano-oficina.png", pngBytes)

	isErr, out, text := call("send", map[string]any{
		"channel": "whatsapp", "account": "personal", "to": "51900@s.whatsapp.net",
		"text": "te paso el plano", "attachments": []string{png},
	})
	if isErr || out["sent"] != false {
		t.Fatalf("plan: %v %s", out, text)
	}
	plan, _ := out["plan"].(map[string]any)
	atts, _ := plan["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("the plan must list each attachment, got %v", plan)
	}
	att := atts[0].(map[string]any)
	if att["name"] != "plano-oficina.png" || att["mime"] != "image/png" || att["size"] != float64(len(pngBytes)) {
		t.Fatalf("attachment info = %v", att)
	}
}

func TestMCPSendAttachmentsConfirmOnPlansOnlyServerSendsNothing(t *testing.T) {
	adapter, call := mediaServiceSession(t, false)
	png := writeTempFile(t, "plano.png", pngBytes)

	isErr, _, text := call("send", map[string]any{
		"channel": "whatsapp", "account": "personal", "to": "51900@s.whatsapp.net",
		"text": "plano", "attachments": []string{png}, "confirm": true,
	})
	if !isErr || !strings.Contains(text, "--allow-send") || !strings.Contains(text, "plano.png") {
		t.Fatalf("confirm on a plans-only server should refuse and show the attachment: %s", text)
	}
	if n := adapter.sends.Load(); n != 0 {
		t.Fatalf("a plans-only server sent %d times", n)
	}
}

func TestMCPSendAttachmentsConfirmedPassesThemThrough(t *testing.T) {
	adapter, call := mediaServiceSession(t, true)
	png := writeTempFile(t, "plano.png", pngBytes)

	// Text may be empty when there is an attachment.
	isErr, out, text := call("send", map[string]any{
		"channel": "whatsapp", "account": "personal", "to": "51900@s.whatsapp.net",
		"attachments": []string{png}, "confirm": true,
	})
	if isErr || out["sent"] != true {
		t.Fatalf("confirmed send: %v %s", out, text)
	}
	if adapter.sends.Load() != 1 || len(adapter.last.Attachments) != 1 || adapter.last.Attachments[0] != png {
		t.Fatalf("adapter got %+v", adapter.last)
	}
}

func TestMCPReplyPassesAttachments(t *testing.T) {
	backend := mcpBackend()
	backend.receipt = core.Receipt{ID: "R1", Channel: core.ChannelWhatsApp}
	s := mcpSession(t, backend, true)
	png := writeTempFile(t, "recibo.png", pngBytes)

	res, out := callTool(t, s, "reply", map[string]any{"id": "whatsapp:personal:1", "text": "adjunto", "attachments": []string{png}, "confirm": true})
	if res.IsError || out["sent"] != true {
		t.Fatalf("reply: %v %s", out, toolText(res))
	}
	if len(backend.replyCalls) != 2 {
		t.Fatalf("reply calls = %+v", backend.replyCalls)
	}
	for _, c := range backend.replyCalls {
		if len(c.Attachments) != 1 || c.Attachments[0] != png {
			t.Fatalf("reply attachments = %+v", c)
		}
	}
}

func TestMCPSendAttachmentPathRules(t *testing.T) {
	backend := mcpBackend()
	s := mcpSession(t, backend, true)
	dir := t.TempDir()
	for name, path := range map[string]string{
		"relative":  "plano.png",
		"home":      "~/plano.png",
		"missing":   filepath.Join(dir, "no-existe.png"),
		"directory": dir,
	} {
		res, _ := callTool(t, s, "send", map[string]any{"channel": "whatsapp", "account": "personal", "to": "jose", "text": "x", "attachments": []string{path}})
		if !res.IsError {
			t.Errorf("%s path %q should be refused", name, path)
		}
		res, _ = callTool(t, s, "reply", map[string]any{"id": "whatsapp:personal:1", "text": "x", "attachments": []string{path}})
		if !res.IsError {
			t.Errorf("reply: %s path %q should be refused", name, path)
		}
	}
	if len(backend.sendCalls) != 0 || len(backend.replyCalls) != 0 {
		t.Fatalf("a bad path must fail before planning: %+v %+v", backend.sendCalls, backend.replyCalls)
	}

	res, _ := callTool(t, s, "send", map[string]any{"channel": "whatsapp", "account": "personal", "to": "jose"})
	if !res.IsError || !strings.Contains(toolText(res), "text") {
		t.Fatalf("a send with neither text nor attachments should be refused: %s", toolText(res))
	}
}

func TestMCPIdempotencyKeyFollowsAttachmentContent(t *testing.T) {
	plan := core.Plan{Action: "send", Recipients: []string{"a"}, Media: []string{"/x/a.png"},
		Attachments: []core.AttachmentInfo{{Name: "a.png", MIME: "image/png", Size: 8}}}
	other := plan
	other.Attachments = []core.AttachmentInfo{{Name: "a.png", MIME: "image/png", Size: 9}}
	if mcpIdempotencyKey(plan) == mcpIdempotencyKey(other) {
		t.Fatal("a file that changed at the same path must not replay the first send")
	}
}
