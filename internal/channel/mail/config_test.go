package mail

import (
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
)

func TestParseAccountConfigDovecotLogin(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":     "mail.example.cl",
			"smtp_host":     "mail.example.cl",
			"username":      "alice@example.cl",
			"auth":          "login",
			"folder_prefix": "INBOX",
			"folder_sep":    ".",
			"password_env":  "/home/alice/.config/bunker-go/mail-cl.env",
		},
	}

	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.IMAPHost != "mail.example.cl" || cfg.IMAPPort != 993 {
		t.Errorf("IMAP addr = %s:%d, want mail.example.cl:993", cfg.IMAPHost, cfg.IMAPPort)
	}
	if cfg.SMTPHost != "mail.example.cl" || cfg.SMTPPort != 587 {
		t.Errorf("SMTP addr = %s:%d, want mail.example.cl:587", cfg.SMTPHost, cfg.SMTPPort)
	}
	if cfg.Auth != AuthLogin {
		t.Errorf("Auth = %v, want AuthLogin", cfg.Auth)
	}
	if cfg.Gmail {
		t.Error("Gmail = true, want false for a Dovecot account")
	}
	if cfg.FolderPrefix != "INBOX" || cfg.FolderSeparator != '.' {
		t.Errorf("folder prefix/sep = %q/%q, want INBOX/.", cfg.FolderPrefix, cfg.FolderSeparator)
	}
	if cfg.EnvPasswordPath == "" {
		t.Error("EnvPasswordPath is empty, want the configured password_env")
	}
}

func TestParseAccountConfigGmailXOAuth2(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "com",
		Options: map[string]interface{}{
			"imap_host":    "imap.gmail.com",
			"smtp_host":    "smtp.gmail.com",
			"username":     "alice@example.com",
			"auth":         "xoauth2",
			"goa_identity": "alice@example.com",
			"gmail":        true,
		},
	}

	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.Auth != AuthXOAuth2 {
		t.Errorf("Auth = %v, want AuthXOAuth2", cfg.Auth)
	}
	if !cfg.Gmail {
		t.Error("Gmail = false, want true")
	}
	if cfg.IMAPPort != 993 || cfg.SMTPPort != 587 {
		t.Errorf("default ports = %d/%d, want 993/587", cfg.IMAPPort, cfg.SMTPPort)
	}
	if cfg.GOAIdentity != "alice@example.com" {
		t.Errorf("GOAIdentity = %q, want alice@example.com", cfg.GOAIdentity)
	}
}

func TestParseAccountConfigMissingHost(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{"username": "x"},
	}
	if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig() error = %v, want ErrInvalidConfig", err)
	}
}

func TestParseAccountConfigDefaultsSeenReconcileInterval(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{"imap_host": "mail.example.cl", "username": "x"},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.SeenReconcileInterval != defaultSeenReconcileInterval {
		t.Errorf("SeenReconcileInterval = %v, want default %v", cfg.SeenReconcileInterval, defaultSeenReconcileInterval)
	}
}

func TestParseAccountConfigCustomSeenReconcileInterval(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":      "mail.example.cl",
			"username":       "x",
			"seen_reconcile": "45s",
		},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.SeenReconcileInterval != 45*time.Second {
		t.Errorf("SeenReconcileInterval = %v, want 45s", cfg.SeenReconcileInterval)
	}
}

func TestParseAccountConfigSeenReconcileIntervalZeroDisables(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":      "mail.example.cl",
			"username":       "x",
			"seen_reconcile": "0",
		},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.SeenReconcileInterval != 0 {
		t.Errorf("SeenReconcileInterval = %v, want 0 (disabled)", cfg.SeenReconcileInterval)
	}
}

func TestParseAccountConfigInvalidSeenReconcileInterval(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":      "mail.example.cl",
			"username":       "x",
			"seen_reconcile": "banana",
		},
	}
	if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig() error = %v, want ErrInvalidConfig", err)
	}
}

func TestParseAccountConfigUnknownAuth(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host": "mail.example.cl",
			"username":  "x",
			"auth":      "carrier-pigeon",
		},
	}
	if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig() error = %v, want ErrInvalidConfig", err)
	}
}

// TestParseAccountConfigInitialSyncLimitDefault: mail-history H4. No
// initial_sync_limit option set must keep the existing 200-message
// default (defaultInitialSyncLimit), so a config.toml written before H4
// keeps behaving exactly as before.
func TestParseAccountConfigInitialSyncLimitDefault(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host": "mail.example.cl",
			"username":  "x",
		},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.InitialSyncLimit != defaultInitialSyncLimit {
		t.Errorf("InitialSyncLimit = %d, want the default %d", cfg.InitialSyncLimit, defaultInitialSyncLimit)
	}
}

func TestParseAccountConfigInitialSyncLimitCustom(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":          "mail.example.cl",
			"username":           "x",
			"initial_sync_limit": int64(500),
		},
	}
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		t.Fatalf("ParseAccountConfig() error = %v", err)
	}
	if cfg.InitialSyncLimit != 500 {
		t.Errorf("InitialSyncLimit = %d, want 500", cfg.InitialSyncLimit)
	}
}

func TestParseAccountConfigInitialSyncLimitZeroIsInvalid(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":          "mail.example.cl",
			"username":           "x",
			"initial_sync_limit": int64(0),
		},
	}
	if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig() error = %v, want ErrInvalidConfig", err)
	}
}

func TestParseAccountConfigInitialSyncLimitNegativeIsInvalid(t *testing.T) {
	acc := config.Account{
		Channel: "mail",
		Name:    "cl",
		Options: map[string]interface{}{
			"imap_host":          "mail.example.cl",
			"username":           "x",
			"initial_sync_limit": int64(-1),
		},
	}
	if _, err := ParseAccountConfig(acc); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("ParseAccountConfig() error = %v, want ErrInvalidConfig", err)
	}
}
