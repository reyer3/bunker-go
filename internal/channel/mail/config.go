package mail

import (
	"errors"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
)

// defaultSeenReconcileInterval is how often Run's periodic \Seen
// safety-net reconcile (R4) re-checks stored-unread messages against
// the server's live flags when an account's config sets no explicit
// seen_reconcile option.
const defaultSeenReconcileInterval = 3 * time.Minute

// ErrInvalidConfig is returned when an account's config.toml options are
// missing a required field or use an unrecognized value.
var ErrInvalidConfig = errors.New("mail: invalid account config")

// AuthMode selects how the adapter authenticates to IMAP/SMTP.
type AuthMode string

const (
	// AuthLogin is username+password LOGIN, used by the .cl Dovecot
	// account.
	AuthLogin AuthMode = "login"
	// AuthXOAuth2 is OAuth2 bearer-token AUTHENTICATE, used by Gmail.
	AuthXOAuth2 AuthMode = "xoauth2"
)

// AccountConfig is a decoded, typed view of one mail account's
// config.toml [[account]] entry.
type AccountConfig struct {
	Name string

	IMAPHost string
	IMAPPort int
	SMTPHost string
	SMTPPort int

	Auth     AuthMode
	Username string

	// Gmail switches on Gmail-only behavior: label organization via
	// X-GM-LABELS instead of IMAP keywords, and skipping the APPEND-to-
	// Sent step on send (Gmail auto-saves sent mail).
	Gmail bool

	// FolderPrefix and FolderSeparator seed the account's FolderMap
	// before any special-use mailboxes are discovered from the server's
	// LIST response (e.g. "INBOX" and '.' for Dovecot, "" and '/' for
	// Gmail).
	FolderPrefix    string
	FolderSeparator byte

	// GOAIdentity is the GNOME Online Accounts PresentationIdentity to
	// discover credentials for. Required when Auth is AuthXOAuth2;
	// optional for AuthLogin, where it is tried before EnvPasswordPath.
	GOAIdentity string
	// EnvPasswordPath is the fallback password file for AuthLogin
	// accounts (see EnvFilePasswordSource).
	EnvPasswordPath string

	// SeenReconcileInterval is how often Run's periodic \Seen safety-net
	// reconcile (R4) UID FETCH FLAGS the newest stored-unread messages
	// and applies any \Seen change made elsewhere that the live IDLE
	// unsolicited-FETCH path (T9b) might have missed. Configured as
	// seen_reconcile, a duration string (e.g. "3m"); defaults to
	// defaultSeenReconcileInterval when the option is absent. Zero
	// (explicitly configured as "0") disables the reconcile entirely.
	SeenReconcileInterval time.Duration

	// InitialSyncLimit bounds how many of the most recent INBOX messages
	// Run backfills on a fresh (never-synced) store, configured as
	// initial_sync_limit. It has no effect once a sync cursor exists:
	// raising it after the first sync only widens a future initial sync
	// of an account newly added afterward. Defaults to
	// defaultInitialSyncLimit (200) when the option is absent; must be
	// > 0 when set.
	InitialSyncLimit int
}

// ParseAccountConfig decodes acc.Options into an AccountConfig, applying
// bunker-go's mail defaults (IMAP port 993, SMTP port 587).
func ParseAccountConfig(acc config.Account) (AccountConfig, error) {
	cfg := AccountConfig{
		Name:     acc.Name,
		IMAPPort: 993,
		SMTPPort: 587,
	}

	cfg.IMAPHost, _ = acc.Options["imap_host"].(string)
	if cfg.IMAPHost == "" {
		return AccountConfig{}, fmt.Errorf("mail: account %q: imap_host is required: %w", acc.Name, ErrInvalidConfig)
	}
	cfg.SMTPHost, _ = acc.Options["smtp_host"].(string)
	if cfg.SMTPHost == "" {
		cfg.SMTPHost = cfg.IMAPHost
	}
	if port, ok := intOption(acc.Options, "imap_port"); ok {
		cfg.IMAPPort = port
	}
	if port, ok := intOption(acc.Options, "smtp_port"); ok {
		cfg.SMTPPort = port
	}

	cfg.Username, _ = acc.Options["username"].(string)
	if cfg.Username == "" {
		return AccountConfig{}, fmt.Errorf("mail: account %q: username is required: %w", acc.Name, ErrInvalidConfig)
	}

	authStr, _ := acc.Options["auth"].(string)
	switch AuthMode(authStr) {
	case AuthLogin, "":
		cfg.Auth = AuthLogin
	case AuthXOAuth2:
		cfg.Auth = AuthXOAuth2
	default:
		return AccountConfig{}, fmt.Errorf("mail: account %q: unknown auth %q: %w", acc.Name, authStr, ErrInvalidConfig)
	}

	cfg.Gmail, _ = acc.Options["gmail"].(bool)

	cfg.FolderPrefix, _ = acc.Options["folder_prefix"].(string)
	sep, _ := acc.Options["folder_sep"].(string)
	if sep != "" {
		cfg.FolderSeparator = sep[0]
	} else {
		cfg.FolderSeparator = '/'
	}

	cfg.GOAIdentity, _ = acc.Options["goa_identity"].(string)
	if cfg.Auth == AuthXOAuth2 && cfg.GOAIdentity == "" {
		return AccountConfig{}, fmt.Errorf("mail: account %q: goa_identity is required for xoauth2: %w", acc.Name, ErrInvalidConfig)
	}
	cfg.EnvPasswordPath, _ = acc.Options["password_env"].(string)

	cfg.SeenReconcileInterval = defaultSeenReconcileInterval
	if raw, ok := acc.Options["seen_reconcile"].(string); ok {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return AccountConfig{}, fmt.Errorf("mail: account %q: seen_reconcile %q: %w", acc.Name, raw, ErrInvalidConfig)
		}
		cfg.SeenReconcileInterval = d
	}

	cfg.InitialSyncLimit = defaultInitialSyncLimit
	if limit, ok := intOption(acc.Options, "initial_sync_limit"); ok {
		if limit <= 0 {
			return AccountConfig{}, fmt.Errorf("mail: account %q: initial_sync_limit must be > 0: %w", acc.Name, ErrInvalidConfig)
		}
		cfg.InitialSyncLimit = limit
	}

	return cfg, nil
}

// intOption reads an integer option that TOML may have decoded as
// int64.
func intOption(options map[string]interface{}, key string) (int, bool) {
	switch v := options[key].(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}
