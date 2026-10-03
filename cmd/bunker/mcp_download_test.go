package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// downloaderAdapter is a mail adapter that serves one attachment's
// bytes, so the download tool runs through a real core.Service (overwrite
// refusal, index checks, 0600 temp-file write) as the daemon does.
type downloaderAdapter struct{ data []byte }

func (a *downloaderAdapter) Channel() core.Channel                      { return core.ChannelMail }
func (a *downloaderAdapter) Account() string                            { return "trabajo" }
func (a *downloaderAdapter) Run(ctx context.Context, _ core.Sink) error { return nil }
func (a *downloaderAdapter) DownloadAttachment(context.Context, core.Item, int) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(a.data)), nil
}

var downloadFixture = []byte("factura de prueba, nada real")

func downloadSession(t *testing.T, allowSend bool) func(args map[string]any) (bool, map[string]any, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	item := core.Item{
		ID: "mail:trabajo:7", Channel: core.ChannelMail, Account: "trabajo", Subject: "factura",
		Attachments: []core.Attachment{{Name: "factura.txt", MIME: "text/plain", Size: int64(len(downloadFixture)), Ref: "1"}},
	}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry()
	reg.Register(&downloaderAdapter{data: downloadFixture})
	s := mcpSession(t, core.NewService(st, reg), allowSend)
	return func(args map[string]any) (bool, map[string]any, string) {
		res, out := callTool(t, s, "download", args)
		return res.IsError, out, toolText(res)
	}
}

func TestMCPDownloadWritesTheAttachment(t *testing.T) {
	// Not gated by --allow-send: nothing leaves the machine.
	call := downloadSession(t, false)
	dest := filepath.Join(t.TempDir(), "factura.txt")

	isErr, out, text := call(map[string]any{"id": "mail:trabajo:7", "path": dest})
	if isErr {
		t.Fatalf("download: %s", text)
	}
	if out["path"] != dest || out["name"] != "factura.txt" || out["mime"] != "text/plain" || out["size"] != float64(len(downloadFixture)) {
		t.Fatalf("download result = %v", out)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, downloadFixture) {
		t.Fatalf("file = %q, %v", got, err)
	}
}

func TestMCPDownloadRefusesOverwriteUnlessForced(t *testing.T) {
	call := downloadSession(t, false)
	dest := filepath.Join(t.TempDir(), "factura.txt")
	if err := os.WriteFile(dest, []byte("previo"), 0o600); err != nil {
		t.Fatal(err)
	}

	isErr, _, text := call(map[string]any{"id": "mail:trabajo:7", "path": dest})
	if !isErr || !strings.Contains(text, "exists") {
		t.Fatalf("an existing file must be refused without force: %s", text)
	}
	if got, _ := os.ReadFile(dest); string(got) != "previo" {
		t.Fatalf("the existing file changed: %q", got)
	}

	isErr, _, text = call(map[string]any{"id": "mail:trabajo:7", "path": dest, "force": true})
	if isErr {
		t.Fatalf("force: %s", text)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, downloadFixture) {
		t.Fatalf("force should overwrite, file = %q", got)
	}
}

func TestMCPDownloadErrors(t *testing.T) {
	call := downloadSession(t, false)
	dir := t.TempDir()
	for name, args := range map[string]map[string]any{
		"bad index":      {"id": "mail:trabajo:7", "index": 3, "path": filepath.Join(dir, "a")},
		"negative index": {"id": "mail:trabajo:7", "index": -1, "path": filepath.Join(dir, "b")},
		"no path":        {"id": "mail:trabajo:7"},
		"relative path":  {"id": "mail:trabajo:7", "path": "factura.txt"},
		"home path":      {"id": "mail:trabajo:7", "path": "~/factura.txt"},
		"missing dir":    {"id": "mail:trabajo:7", "path": filepath.Join(dir, "no-existe", "a")},
	} {
		if isErr, _, text := call(args); !isErr {
			t.Errorf("%s should fail, got %s", name, text)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "no-existe")); !os.IsNotExist(err) {
		t.Error("download must not create missing parent directories, like bunker download")
	}
}

func TestMCPDownloadAnnotations(t *testing.T) {
	s := mcpSession(t, mcpBackend(), false)
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "download" {
			continue
		}
		a := tool.Annotations
		if a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil {
			t.Fatalf("download writes a local file: not read-only, not destructive, got %+v", a)
		}
		return
	}
	t.Fatal("download tool missing")
}
