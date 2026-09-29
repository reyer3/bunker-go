// Package config loads bunker-go's TOML configuration and resolves its
// config/state directories, both overridable by environment variables so
// tests never touch a real home directory.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Account is one configured channel account. Channel-specific settings
// (IMAP host, WhatsApp device path, Matrix homeserver, ...) are kept in
// Options as a raw map so adapters decode only what they need, without
// core or config knowing every channel's shape.
type Account struct {
	Channel string
	Name    string
	Options map[string]interface{}
}

// Config is the parsed contents of config.toml.
type Config struct {
	Accounts []Account
	Render   Render
	Tui      Tui
	App      App
	Herdr    Herdr
}

// Render holds optional status-line presentation settings.
type Render struct {
	// Glyphs overrides the styled render glyph per channel ("mail",
	// "whatsapp", "matrix"), e.g. a codepoint from a locally installed
	// icon font.
	Glyphs map[string]string
}

// Tui holds optional interactive-panel settings.
type Tui struct {
	// Notify opts out of desktop notifications (OSC 777) when set to an
	// explicit false; nil (the key absent) means the default, enabled.
	// BUNKER_TUI_NOTIFY=0 is a second, independent opt-out.
	Notify *bool
}

// Herdr holds the settings of bunker as a herdr side panel.
type Herdr struct {
	// Notify makes "bunker sidebar", when it runs inside herdr, announce
	// new messages as herdr notifications. Off by default: it is opt-in
	// because herdr plays a sound with each one.
	Notify bool
}

// App holds the settings of "bunker app", the TUI in its own window.
type App struct {
	// Command replaces the terminal command line that opens the window.
	// Its last element is the program the terminal runs, normally the
	// bunker binary itself; empty means the built-in terminal choice.
	Command []string
}

type rawConfig struct {
	Account []map[string]interface{} `toml:"account"`
	Render  struct {
		Glyphs map[string]string `toml:"glyphs"`
	} `toml:"render"`
	Tui struct {
		Notify *bool `toml:"notify"`
	} `toml:"tui"`
	App struct {
		Command []string `toml:"command"`
	} `toml:"app"`
	Herdr struct {
		Notify bool `toml:"notify"`
	} `toml:"herdr"`
}

// Load parses the TOML file at path into a Config.
func Load(path string) (*Config, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("config: load %s: %w", path, err)
	}

	cfg := &Config{
		Accounts: make([]Account, 0, len(raw.Account)),
		Render:   Render{Glyphs: raw.Render.Glyphs},
		Tui:      Tui{Notify: raw.Tui.Notify},
		App:      App{Command: raw.App.Command},
		Herdr:    Herdr{Notify: raw.Herdr.Notify},
	}
	for _, entry := range raw.Account {
		acc := Account{Options: make(map[string]interface{})}
		for k, v := range entry {
			switch k {
			case "channel":
				if s, ok := v.(string); ok {
					acc.Channel = s
				}
			case "name":
				if s, ok := v.(string); ok {
					acc.Name = s
				}
			default:
				acc.Options[k] = v
			}
		}
		cfg.Accounts = append(cfg.Accounts, acc)
	}
	return cfg, nil
}

// LoadDefault parses the config file at ConfigPath().
func LoadDefault() (*Config, error) {
	return Load(ConfigPath())
}

// ConfigDir returns BUNKER_CONFIG_DIR if set, else ~/.config/bunker-go.
func ConfigDir() string {
	if dir := os.Getenv("BUNKER_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "bunker-go")
	}
	return filepath.Join(home, ".config", "bunker-go")
}

// StateDir returns BUNKER_STATE_DIR if set, else ~/.local/state/bunker-go.
func StateDir() string {
	if dir := os.Getenv("BUNKER_STATE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".local", "state", "bunker-go")
	}
	return filepath.Join(home, ".local", "state", "bunker-go")
}

// ConfigPath returns ConfigDir()/config.toml.
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.toml")
}

// StoreDBPath returns StateDir()/bunker.db, the default SQLite store path.
func StoreDBPath() string {
	return filepath.Join(StateDir(), "bunker.db")
}

// CacheDir returns BUNKER_CACHE_DIR if set, else ~/.cache/bunker-go.
// Unlike ConfigDir/StateDir, its contents are disposable: losing it only
// means avatars are re-fetched, nothing more.
func CacheDir() string {
	if dir := os.Getenv("BUNKER_CACHE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".cache", "bunker-go")
	}
	return filepath.Join(home, ".cache", "bunker-go")
}

// AvatarCacheDir returns CacheDir()/avatars, where Service.Avatar caches
// thumbnails and generated fallbacks (see internal/core/avatar.go).
func AvatarCacheDir() string {
	return filepath.Join(CacheDir(), "avatars")
}
