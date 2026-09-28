package mail

import (
	"context"
	"crypto/tls"
	"log/slog"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2/imapclient"
)

// defaultInitialSyncLimit bounds how many of the most recent INBOX
// messages Adapter.Run backfills on startup.
const defaultInitialSyncLimit = 200

// mailHealthySession is how long one runOnce call (R2) must have lasted
// for Run to treat the connection as healthy and reset its attempt
// count, mirroring the daemon adapter supervisor's own reset threshold
// (cmd/bunker's adapterHealthyRun): without this, attempt only ever grew,
// so after enough reconnects over the account's lifetime every
// subsequent reconnect waited the 5-minute cap even right after a long,
// otherwise-healthy IDLE session.
const mailHealthySession = 2 * time.Minute

// dialFunc opens an authenticated IMAP connection for cfg. It exists so
// tests can substitute a plaintext dial against imapmemserver for the
// real TLS dial (dialReal) production uses; handler receives unilateral
// mailbox updates (new-message notifications while idling).
type dialFunc func(ctx context.Context, cfg AccountConfig, passwordSource PasswordSource, tokenSource TokenSource, handler *imapclient.UnilateralDataHandler) (*imapclient.Client, error)

// Adapter is the mail channel's core.Adapter: IMAP for receive/organize,
// SMTP for send. It also implements core.Fetcher, core.Sender and
// core.Organizer.
type Adapter struct {
	cfg            AccountConfig
	passwordSource PasswordSource
	tokenSource    TokenSource

	dial             dialFunc
	smtpDial         smtpDialFunc
	now              func() time.Time
	backoff          func(attempt int) time.Duration
	initialSyncLimit uint32

	// logger receives Run's lifecycle logging (R2): a runOnce failure,
	// previously discarded, is logged at error level with channel/
	// account attributes. It defaults to slog.Default() (stderr), same
	// as every other channel's adapter supervision logging.
	logger *slog.Logger

	// gmailTLSConfig is nil in production (default TLS verification);
	// tests set it to skip verifying a fake server's self-signed cert.
	gmailTLSConfig *tls.Config
}

var (
	_ core.Adapter              = (*Adapter)(nil)
	_ core.Fetcher              = (*Adapter)(nil)
	_ core.Sender               = (*Adapter)(nil)
	_ core.Organizer            = (*Adapter)(nil)
	_ core.FolderMover          = (*Adapter)(nil)
	_ core.AttachmentDownloader = (*Adapter)(nil)
)

// NewAdapter builds the mail Adapter for one configured account. It
// never dials IMAP or SMTP itself, and it never touches GNOME Online
// Accounts until Run, Fetch, Send or Organize actually needs a
// credential.
func NewAdapter(acc config.Account) (core.Adapter, error) {
	cfg, err := ParseAccountConfig(acc)
	if err != nil {
		return nil, err
	}

	bus := &lazyGOABus{}
	var passwordSource PasswordSource
	var tokenSource TokenSource
	switch cfg.Auth {
	case AuthXOAuth2:
		tokenSource = GOATokenSource{Bus: bus, Identity: cfg.GOAIdentity}
	case AuthLogin:
		var sources FallbackPasswordSource
		if cfg.GOAIdentity != "" {
			sources = append(sources, GOAPasswordSource{Bus: bus, Identity: cfg.GOAIdentity})
		}
		if cfg.EnvPasswordPath != "" {
			sources = append(sources, EnvFilePasswordSource{Path: cfg.EnvPasswordPath})
		}
		passwordSource = sources
	}

	return newAdapter(cfg, passwordSource, tokenSource, dialReal), nil
}

// newAdapter is the fully-injectable constructor tests use to swap in a
// fake dialer, credential sources and clocks. cfg.InitialSyncLimit
// overrides the 200-message default (H4) when it is set (> 0); most
// existing tests build an AccountConfig by hand without going through
// ParseAccountConfig, so its zero value must keep meaning "use the
// default", never "sync nothing".
func newAdapter(cfg AccountConfig, passwordSource PasswordSource, tokenSource TokenSource, dial dialFunc) *Adapter {
	a := &Adapter{
		cfg:              cfg,
		passwordSource:   passwordSource,
		tokenSource:      tokenSource,
		dial:             dial,
		smtpDial:         smtpDialReal,
		now:              time.Now,
		backoff:          defaultBackoff,
		initialSyncLimit: defaultInitialSyncLimit,
		logger:           slog.Default(),
	}
	if cfg.InitialSyncLimit > 0 {
		a.initialSyncLimit = uint32(cfg.InitialSyncLimit)
	}
	return a
}

// Channel implements core.Adapter.
func (a *Adapter) Channel() core.Channel { return core.ChannelMail }

// Account implements core.Adapter.
func (a *Adapter) Account() string { return a.cfg.Name }

// defaultBackoff is a full-jitter-free exponential backoff capped at 5
// minutes: 1s, 2s, 4s, ... 300s.
func defaultBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
