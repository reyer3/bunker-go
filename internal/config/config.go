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
}

type rawConfig struct {
	Account []map[string]interface{} `toml:"account"`
}

// Load parses the TOML file at path into a Config.
func Load(path string) (*Config, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("config: load %s: %w", path, err)
	}

	cfg := &Config{Accounts: make([]Account, 0, len(raw.Account))}
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
