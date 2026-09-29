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

	// IndexBodyMaxKB caps how many KiB of each message's text sync
	// fetches and stores so full-text search finds mail by its body
	// (#91), configured as index_body_max_kb. ParseAccountConfig
	// defaults it to defaultIndexBodyMaxKB (64); 0 disables it, leaving
	// synced mail with an empty body until it is read. Unlike
	// InitialSyncLimit, the zero value here means off, so hand-built test
	// configs keep the header-only sync they were written against.
	IndexBodyMaxKB int

	// SyncFolders, configured as sync_folders, lists the folders Run
	// keeps in sync besides INBOX and Sent (which are always synced),
	// replacing the default choice (see planSyncFolders). Each entry is
	// resolved like a move target: a friendly name ("Archive"), a name
	// under the prefix ("Clients.Acme") or the server's full name.
	SyncFolders []string
	// ExcludeFolders, configured as exclude_folders, lists folders (and,
	// with them, every folder under each) never synced, on top of the
	// built-in Trash/Junk/Drafts exclusions.
	ExcludeFolders []string
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

	cfg.IndexBodyMaxKB = defaultIndexBodyMaxKB
	if kb, ok := intOption(acc.Options, "index_body_max_kb"); ok {
		if kb < 0 {
			return AccountConfig{}, fmt.Errorf("mail: account %q: index_body_max_kb must be >= 0: %w", acc.Name, ErrInvalidConfig)
		}
		cfg.IndexBodyMaxKB = kb
	}

	for key, dst := range map[string]*[]string{
		"sync_folders":    &cfg.SyncFolders,
		"exclude_folders": &cfg.ExcludeFolders,
	} {
		list, err := stringListOption(acc.Options, key)
		if err != nil {
			return AccountConfig{}, fmt.Errorf("mail: account %q: %s: %v: %w", acc.Name, key, err, ErrInvalidConfig)
		}
		*dst = list
	}

	return cfg, nil
}

// stringListOption reads an optional list-of-strings option, which TOML
// decodes as []interface{}. A present option that is not a list of
// non-empty strings is an error rather than ignored, so a typo like
// sync_folders = "Archive" is caught at startup instead of silently
// syncing the default set.
func stringListOption(options map[string]interface{}, key string) ([]string, error) {
	raw, ok := options[key]
	if !ok {
		return nil, nil
	}
	var items []interface{}
	switch v := raw.(type) {
	case []interface{}:
		items = v
	case []string:
		for _, s := range v {
			items = append(items, s)
		}
	default:
		return nil, fmt.Errorf("want a list of folder names, got %T", raw)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("want non-empty folder names, got %v", item)
		}
		out = append(out, s)
	}
	return out, nil
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
