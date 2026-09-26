package whatsapp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	_ "modernc.org/sqlite" // pure-Go sqlite driver, registered as "sqlite"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/secfile"
)

// realClient adapts *whatsmeow.Client to waClient. Every promoted method
// (Connect, Disconnect, SendMessage, ...) matches waClient's signature
// exactly except MarkRead, which whatsmeow declares with a trailing
// variadic receipt-type override this adapter never needs; the explicit
// method below shadows the promoted one to drop it.
type realClient struct {
	*whatsmeow.Client
}

func (r *realClient) IsLinked() bool {
	return r.Client.Store != nil && r.Client.Store.ID != nil
}

func (r *realClient) OwnJID() types.JID {
	if r.Client.Store == nil || r.Client.Store.ID == nil {
		return types.JID{}
	}
	return *r.Client.Store.ID
}

func (r *realClient) MarkRead(ctx context.Context, ids []types.MessageID, timestamp time.Time, chat, sender types.JID) error {
	return r.Client.MarkRead(ctx, ids, timestamp, chat, sender)
}

var _ waClient = (*realClient)(nil)

// buildRealClient opens acc's device store (pure-Go modernc.org/sqlite,
// no CGO) and wraps its whatsmeow client as a waClient. It never
// connects; callers decide when to Run or Link.
func buildRealClient(acc config.Account) (*realClient, error) {
	dbPath := stringOption(acc.Options, "db_path", filepath.Join(config.StateDir(), fmt.Sprintf("whatsapp-%s.db", acc.Name)))

	// The device store holds session keys and message history, so it is
	// created (or tightened) private before whatsmeow ever touches it:
	// see internal/secfile for why a DSN string alone cannot do this.
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := secfile.EnsureDir(dir); err != nil {
			return nil, fmt.Errorf("whatsapp: create state dir: %w", err)
		}
	}
	if err := secfile.EnsureFile(dbPath); err != nil {
		return nil, fmt.Errorf("whatsapp: create private device store %s: %w", dbPath, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)

	container, err := sqlstore.New(context.Background(), "sqlite", dsn, waLog.Noop)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: open device store %s: %w", dbPath, err)
	}
	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		return nil, fmt.Errorf("whatsapp: load device: %w", err)
	}
	secfile.SecureSidecars(dbPath, "-wal", "-shm")
	return &realClient{whatsmeow.NewClient(device, waLog.Noop)}, nil
}

// NewFromAccount builds a core.Adapter for a configured WhatsApp account.
// Its signature matches cmd/bunker's adapterConstructor exactly; the
// integrator (T5) registers it with RegisterAdapter("whatsapp",
// whatsapp.NewFromAccount) from inside cmd/bunker. This package cannot
// do that registration itself in an init(): cmd/bunker is package main,
// and Go refuses to import a main package from anywhere else (verified:
// "import ... is a program, not an importable package"). See the feature
// doc's open question about this.
func NewFromAccount(acc config.Account) (core.Adapter, error) {
	cli, err := buildRealClient(acc)
	if err != nil {
		return nil, err
	}
	interval := durationOption(acc.Options, "min_send_interval_seconds", defaultMinSendInterval)
	adapter := NewAdapter(acc.Name, cli, interval)
	adapter.SetNameResolver(newStoreNameResolver(cli.Client))
	adapter.SetHistoryLimit(intOption(acc.Options, "history_messages_per_chat", defaultHistoryMessagesPerChat))
	// Fan-out limits (T13a) are read straight through, left at zero when
	// unconfigured: core.Service.Send/Reply already fall back to its own
	// built-in 10-recipient/3-8s default per zero field, so this never
	// duplicates those constants.
	adapter.SetFanoutPolicy(core.FanoutPolicy{
		MaxRecipients: intOption(acc.Options, "max_broadcast_recipients", 0),
		PauseMin:      durationOption(acc.Options, "broadcast_pause_min_seconds", 0),
		PauseMax:      durationOption(acc.Options, "broadcast_pause_max_seconds", 0),
	})
	return adapter, nil
}

// Link runs the QR-pairing handshake for acc and renders each code to
// out, for a future "bunker link whatsapp" command. It never runs
// against the network in a test: tests exercise linkWithClient directly
// with a fake waClient and a fake QR channel.
func Link(ctx context.Context, acc config.Account, out io.Writer) error {
	cli, err := buildRealClient(acc)
	if err != nil {
		return err
	}
	return linkWithClient(ctx, cli, out)
}

// stringOption reads a string option from a config.Account.Options map,
// falling back to def when absent or of the wrong type.
func stringOption(opts map[string]interface{}, key, def string) string {
	if v, ok := opts[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// intOption reads an integer option from a config.Account.Options map,
// the same way durationOption reads a duration one: TOML decodes
// integers as int64 and fractional numbers as float64, and both are
// accepted here (a float is truncated). Anything else, including a
// missing key, returns def.
func intOption(opts map[string]interface{}, key string, def int) int {
	v, ok := opts[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return def
	}
}

// durationOption reads a "<key> seconds" numeric option. TOML decodes
// integers as int64 and fractional numbers as float64; both are
// accepted. Anything else, including a missing key, returns def.
func durationOption(opts map[string]interface{}, key string, def time.Duration) time.Duration {
	v, ok := opts[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int64:
		return time.Duration(n) * time.Second
	case float64:
		return time.Duration(n * float64(time.Second))
	default:
		return def
	}
}
