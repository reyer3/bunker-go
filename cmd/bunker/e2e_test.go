package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI drives the real run() entry point (env-resolved socket -> rpc.Dial
// -> runWithBackend, exactly what a person or Claude Code invokes) and
// returns its exit code and stdout as a string, for assertions.
func runCLI(t *testing.T, args []string) (int, string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}

	code := run(args, os.Stdin, stdoutW, stderrW)
	stdoutW.Close()
	stderrW.Close()

	outBuf := make([]byte, 65536)
	n, _ := stdoutR.Read(outBuf)
	errBuf := make([]byte, 65536)
	m, _ := stderrR.Read(errBuf)

	if code != 0 {
		t.Logf("runCLI(%v) exit=%d stderr=%s", args, code, string(errBuf[:m]))
	}
	return code, string(outBuf[:n])
}

// TestEndToEndCLIAgainstFakeDaemon starts the real daemon (runDaemon, the
// same code cmdDaemonMain calls) wired to the in-memory fake adapter over
// a temp unix socket, then drives list/read/reply --dry-run/organize
// --dry-run/counts/render through the actual CLI entry point with --json,
// exactly as Claude Code or Alice would from a terminal. It never opens
// a real mail/WhatsApp/Matrix account: the fake channel is the only
// adapter running.
func TestEndToEndCLIAgainstFakeDaemon(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "bunker.sock")
	t.Setenv("BUNKER_SOCKET", socket)
	t.Setenv("BUNKER_STATE_DIR", dir)

	ctx, cancel := context.WithCancel(context.Background())
	daemonDone := make(chan error, 1)
	daemonOut := &bytes.Buffer{}
	daemonErr := &bytes.Buffer{}
	go func() {
		daemonDone <- runDaemon(ctx, dir, filepath.Join(dir, "avatars"), socket, true, daemonOut, daemonErr)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-daemonDone; err != nil {
			t.Errorf("runDaemon: %v", err)
		}
	})

	dialUntilReady(t, socket).Close()

	// list --json: --fake seeds one demo item per channel, pushed
	// asynchronously by each adapter's Run loop, so poll until all three
	// have landed instead of racing the first list call against them.
	var listResp struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"items"`
	}
	var out string
	var code int
	pollUntil(daemonStartBudget, func() bool {
		code, out = runCLI(t, []string{"list", "--json"})
		if code != 0 {
			t.Fatalf("list --json exit code = %d", code)
		}
		if err := json.Unmarshal([]byte(out), &listResp); err != nil {
			t.Fatalf("unmarshal list output %q: %v", out, err)
		}
		return len(listResp.Items) == 3
	})
	if len(listResp.Items) != 3 {
		t.Fatalf("list --json returned %d items, want 3 (one per demo channel): %s", len(listResp.Items), out)
	}
	const mailID = "mail:demo:1"
	found := false
	for _, it := range listResp.Items {
		if it.ID == mailID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list --json = %s, want it to contain %q", out, mailID)
	}

	// find and list --query go through the daemon's query parser: one
	// channel's item, then two pages of one item that together hold all
	// three, and an unknown operator as a loud error.
	var page struct {
		Items []struct {
			ID string `json:"ID"`
		} `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	code, out = runCLI(t, []string{"find", "channel:mail", "--json"})
	if err := json.Unmarshal([]byte(out), &page); code != 0 || err != nil || len(page.Items) != 1 || page.Items[0].ID != mailID || page.NextCursor != "" {
		t.Fatalf("find channel:mail --json = %d %q (%v)", code, out, err)
	}
	code, out = runCLI(t, []string{"list", "--query", "-channel:mail", "--limit", "1", "--json"})
	if err := json.Unmarshal([]byte(out), &page); code != 0 || err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("list --query page 1 = %d %q (%v)", code, out, err)
	}
	first := page.Items[0].ID
	code, out = runCLI(t, []string{"list", "--query", "-channel:mail", "--limit", "1", "--cursor", page.NextCursor, "--json"})
	page.NextCursor = ""
	if err := json.Unmarshal([]byte(out), &page); code != 0 || err != nil || len(page.Items) != 1 || page.Items[0].ID == first || page.Items[0].ID == mailID || page.NextCursor != "" {
		t.Fatalf("list --query page 2 = %d %q (%v)", code, out, err)
	}
	if code, out = runCLI(t, []string{"find", "foo:bar", "--json"}); code == 0 || !strings.Contains(out, `foo:`) {
		t.Fatalf("find foo:bar = %d %q, want an error naming the operator", code, out)
	}

	// read <id> --json
	code, out = runCLI(t, []string{"read", mailID, "--json"})
	if code != 0 {
		t.Fatalf("read --json exit code = %d", code)
	}
	if !strings.Contains(out, mailID) {
		t.Fatalf("read --json = %q, want it to contain %q", out, mailID)
	}

	// reply <id> <text> --dry-run --json: the dry-run gate must return a
	// plan and never a real receipt.
	code, out = runCLI(t, []string{"reply", mailID, "dry run reply", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("reply --dry-run --json exit code = %d, out = %s", code, out)
	}
	var replyResp struct {
		DryRun  bool `json:"dryRun"`
		Receipt struct {
			ID string `json:"ID"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(out), &replyResp); err != nil {
		t.Fatalf("unmarshal reply output %q: %v", out, err)
	}
	if !replyResp.DryRun {
		t.Fatalf("reply --dry-run --json = %s, want dryRun: true", out)
	}
	if replyResp.Receipt.ID != "" {
		t.Fatalf("reply --dry-run --json = %s, want a zero-value receipt (adapter must never be called)", out)
	}

	// organize <id> --label vip --dry-run --json
	code, out = runCLI(t, []string{"organize", mailID, "--label", "vip", "--dry-run", "--json"})
	if code != 0 {
		t.Fatalf("organize --dry-run --json exit code = %d, out = %s", code, out)
	}
	var organizeResp struct {
		DryRun bool `json:"dryRun"`
	}
	if err := json.Unmarshal([]byte(out), &organizeResp); err != nil {
		t.Fatalf("unmarshal organize output %q: %v", out, err)
	}
	if !organizeResp.DryRun {
		t.Fatalf("organize --dry-run --json = %s, want dryRun: true", out)
	}

	// counts --json
	code, out = runCLI(t, []string{"counts", "--json"})
	if code != 0 {
		t.Fatalf("counts --json exit code = %d", code)
	}
	var countsResp struct {
		Counts map[string]map[string]int `json:"counts"`
	}
	if err := json.Unmarshal([]byte(out), &countsResp); err != nil {
		t.Fatalf("unmarshal counts output %q: %v", out, err)
	}
	if countsResp.Counts["mail"]["demo"] != 1 {
		t.Fatalf("counts --json = %s, want mail/demo: 1", out)
	}

	// render --json: with the daemon up, it must report daemonUp: true and
	// one segment per channel.
	code, out = runCLI(t, []string{"render", "--json"})
	if code != 0 {
		t.Fatalf("render --json exit code = %d", code)
	}
	var renderResp struct {
		Segments []struct {
			Channel string `json:"channel"`
			Unread  int    `json:"unread"`
		} `json:"segments"`
		DaemonUp bool `json:"daemonUp"`
	}
	if err := json.Unmarshal([]byte(out), &renderResp); err != nil {
		t.Fatalf("unmarshal render output %q: %v", out, err)
	}
	if !renderResp.DaemonUp {
		t.Fatalf("render --json = %s, want daemonUp: true", out)
	}
	if len(renderResp.Segments) != 3 {
		t.Fatalf("render --json = %s, want 3 segments", out)
	}
}
