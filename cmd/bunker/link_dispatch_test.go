package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDispatchesLinkThroughRealConfig proves run()'s own wiring for
// "link" (env-resolved config path -> config.LoadDefault -> cmdLink)
// against a real config.toml, not just cmdLink's unit tests above.
func TestRunDispatchesLinkThroughRealConfig(t *testing.T) {
	dir := t.TempDir()
	configToml := "[[account]]\nchannel = \"whatsapp\"\nname = \"personal\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(configToml), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("BUNKER_CONFIG_DIR", dir)

	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	code := run([]string{"link", "whatsapp", "ghost"}, os.Stdin, os.Stdout, stderrW)
	stderrW.Close()

	buf := make([]byte, 4096)
	n, _ := stderrR.Read(buf)
	stderr := string(buf[:n])

	if code != 1 {
		t.Fatalf("run() exit code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "ghost") {
		t.Fatalf("stderr = %q, want it to name the missing account (proves config.toml was actually loaded)", stderr)
	}
}
