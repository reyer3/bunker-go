package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCacheDirRespectsEnvOverride(t *testing.T) {
	t.Setenv("BUNKER_CACHE_DIR", "/tmp/bunker-test-cache")
	if got := config.CacheDir(); got != "/tmp/bunker-test-cache" {
		t.Fatalf("CacheDir() = %q, want override", got)
	}
}

func TestCacheDirDefaultsUnderHome(t *testing.T) {
	t.Setenv("BUNKER_CACHE_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir available: %v", err)
	}
	want := filepath.Join(home, ".cache", "bunker-go")
	if got := config.CacheDir(); got != want {
		t.Fatalf("CacheDir() = %q, want %q", got, want)
	}
}

func TestAvatarCacheDirJoinsCacheDir(t *testing.T) {
	t.Setenv("BUNKER_CACHE_DIR", "/tmp/bunker-test-cache")
	want := filepath.Join("/tmp/bunker-test-cache", "avatars")
	if got := config.AvatarCacheDir(); got != want {
		t.Fatalf("AvatarCacheDir() = %q, want %q", got, want)
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

func TestLoadDefaultsTuiNotifyToNilMeaningEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[[account]]\nchannel = \"mail\"\nname = \"cl\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tui.Notify != nil {
		t.Errorf("Tui.Notify = %v, want nil (unset means the default, enabled)", cfg.Tui.Notify)
	}
}

func TestLoadReadsTuiNotifyFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[tui]\nnotify = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tui.Notify == nil || *cfg.Tui.Notify {
		t.Errorf("Tui.Notify = %v, want an explicit false", cfg.Tui.Notify)
	}
}

func TestLoadReadsTuiConfirmChatSend(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]bool{
		"[[account]]\nchannel = \"mail\"\nname = \"cl\"\n": false,
		"[tui]\nconfirm_chat_send = true\n":                true,
	} {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Tui.ConfirmChatSend != want {
			t.Errorf("Tui.ConfirmChatSend = %v for %q, want %v", cfg.Tui.ConfirmChatSend, body, want)
		}
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

func TestLoadReadsRenderColorOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[render.glyphs]\nmatrix = \"x\"\n\n[render.colors]\nmail = \"#7aa2f7\"\ndim = \"#565f89\"\n\n[[account]]\nchannel = \"mail\"\nname = \"cl\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Render.Colors["mail"]; got != "#7aa2f7" {
		t.Errorf("Render.Colors[mail] = %q, want #7aa2f7", got)
	}
	if got := cfg.Render.Colors["dim"]; got != "#565f89" {
		t.Errorf("Render.Colors[dim] = %q, want #565f89", got)
	}
	if got := cfg.Render.Glyphs["matrix"]; got != "x" {
		t.Errorf("Render.Glyphs[matrix] = %q, want x (colors must not disturb glyphs)", got)
	}
}

func TestLoadReadsAppCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[app]\ncommand = [\"kitty\", \"--class\", \"dev.bunker.app\", \"bunker\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Join(cfg.App.Command, " ") != "kitty --class dev.bunker.app bunker" {
		t.Errorf("App.Command = %q", cfg.App.Command)
	}
}

func TestLoadReadsHerdrNotifyOffByDefault(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"", false},
		{"[herdr]\nnotify = true\n", true},
		{"[herdr]\nnotify = false\n", false},
	} {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", tc.body, err)
		}
		if cfg.Herdr.Notify != tc.want {
			t.Errorf("Load(%q): Herdr.Notify = %v, want %v", tc.body, cfg.Herdr.Notify, tc.want)
		}
	}
}

func TestLoadReadsUpdateCheckOnByDefault(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"", true},
		{"[update]\ncheck = true\n", true},
		{"[update]\ncheck = false\n", false},
	} {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", tc.body, err)
		}
		if cfg.Update.Check != tc.want {
			t.Errorf("Load(%q): Update.Check = %v, want %v", tc.body, cfg.Update.Check, tc.want)
		}
	}
}
