package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
)

func TestLoadParsesAccountsWithChannelSpecificKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[[account]]
channel = "mail"
name = "cl"
imap_host = "imap.example.cl"
imap_port = 993

[[account]]
channel = "whatsapp"
name = "personal"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("Accounts len = %d, want 2", len(cfg.Accounts))
	}

	mail := cfg.Accounts[0]
	if mail.Channel != "mail" || mail.Name != "cl" {
		t.Fatalf("first account = %+v, want mail/cl", mail)
	}
	if mail.Options["imap_host"] != "imap.example.cl" {
		t.Fatalf("Options[imap_host] = %v, want imap.example.cl", mail.Options["imap_host"])
	}
	if _, hasChannel := mail.Options["channel"]; hasChannel {
		t.Fatalf("Options must not repeat the channel key: %+v", mail.Options)
	}

	wa := cfg.Accounts[1]
	if wa.Channel != "whatsapp" || wa.Name != "personal" {
		t.Fatalf("second account = %+v, want whatsapp/personal", wa)
	}
}

func TestLoadMissingFileReturnsError(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestConfigDirAndStateDirRespectEnvOverrides(t *testing.T) {
	t.Setenv("BUNKER_CONFIG_DIR", "/tmp/bunker-test-config")
	t.Setenv("BUNKER_STATE_DIR", "/tmp/bunker-test-state")

	if got := config.ConfigDir(); got != "/tmp/bunker-test-config" {
		t.Fatalf("ConfigDir() = %q, want override", got)
	}
	if got := config.StateDir(); got != "/tmp/bunker-test-state" {
		t.Fatalf("StateDir() = %q, want override", got)
	}
}

func TestConfigPathJoinsConfigDir(t *testing.T) {
	t.Setenv("BUNKER_CONFIG_DIR", "/tmp/bunker-test-config")
	want := filepath.Join("/tmp/bunker-test-config", "config.toml")
	if got := config.ConfigPath(); got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestConfigDirDefaultsUnderHome(t *testing.T) {
	t.Setenv("BUNKER_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir available: %v", err)
	}
	want := filepath.Join(home, ".config", "bunker-go")
	if got := config.ConfigDir(); got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestLoadReadsRenderGlyphOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[render.glyphs]\nmatrix = \"\\U00100000\"\n\n[[account]]\nchannel = \"mail\"\nname = \"cl\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Render.Glyphs["matrix"]; got != "\U00100000" {
		t.Errorf("Render.Glyphs[matrix] = %q, want U+100000", got)
	}
	if len(cfg.Accounts) != 1 {
		t.Errorf("accounts = %d, want 1 (render section must not disturb accounts)", len(cfg.Accounts))
	}
}
