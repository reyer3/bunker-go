package mail

import (
	"errors"
	"testing"

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
