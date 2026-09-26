package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// TestSQLiteDeviceStoreIsPureGo proves the whatsmeow device store opens
// on modernc.org/sqlite (registered as driver "sqlite" by this package's
// import of it), with no CGO sqlite3 driver involved anywhere in the
// build. A container in a fresh temp dir must open, upgrade its schema
// and hand back an unregistered (unlinked) device.
func TestSQLiteDeviceStoreIsPureGo(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "whatsmeow.db") + "?_pragma=foreign_keys(1)"

	container, err := sqlstore.New(context.Background(), "sqlite", dsn, waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New() error = %v", err)
	}
	defer container.Close()

	device, err := container.GetFirstDevice(context.Background())
	if err != nil {
		t.Fatalf("GetFirstDevice() error = %v", err)
	}
	if device.ID != nil {
		t.Errorf("device.ID = %v, want nil for a freshly created device", device.ID)
	}
}

func TestNewFromAccountBuildsAnAdapter(t *testing.T) {
	dir := t.TempDir()
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{
			"db_path":                   filepath.Join(dir, "wa.db"),
			"min_send_interval_seconds": int64(5),
		},
	}

	adapter, err := NewFromAccount(acc)
	if err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}
	if adapter.Channel() != "whatsapp" {
		t.Errorf("Channel() = %v", adapter.Channel())
	}
	if adapter.Account() != "personal" {
		t.Errorf("Account() = %v", adapter.Account())
	}
	wa, ok := adapter.(*Adapter)
	if !ok {
		t.Fatalf("adapter is %T, want *Adapter", adapter)
	}
	if wa.minSendInterval != 5*time.Second {
		t.Errorf("minSendInterval = %v, want 5s from config", wa.minSendInterval)
	}
}

// TestNewFromAccountCreatesPrivateDeviceDB covers the live-link finding
// (2026-09-25): the whatsmeow device store was created 0644 with a loose
// parent directory. NewFromAccount must leave the device DB file at 0600
// and its (possibly newly created) parent directory at 0700.
func TestNewFromAccountCreatesPrivateDeviceDB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits are not meaningful on Windows")
	}
	root := t.TempDir()
	dbDir := filepath.Join(root, "nested")
	dbPath := filepath.Join(dbDir, "wa.db")
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{"db_path": dbPath},
	}

	if _, err := NewFromAccount(acc); err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}

	dirInfo, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("device store dir mode = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat db file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("device store file mode = %o, want 0600", perm)
	}
}

// TestNewFromAccountAppliesFanoutOptions covers T13(a)'s per-account
// broadcast limits: max_broadcast_recipients and broadcast_pause_min/max
// _seconds reach the adapter's core.FanoutPolicy.
func TestNewFromAccountAppliesFanoutOptions(t *testing.T) {
	dir := t.TempDir()
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{
			"db_path":                     filepath.Join(dir, "wa.db"),
			"max_broadcast_recipients":    int64(5),
			"broadcast_pause_min_seconds": int64(2),
			"broadcast_pause_max_seconds": int64(4),
		},
	}

	adapter, err := NewFromAccount(acc)
	if err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}
	wa, ok := adapter.(*Adapter)
	if !ok {
		t.Fatalf("adapter is %T, want *Adapter", adapter)
	}
	policy := wa.FanoutPolicy()
	if policy.MaxRecipients != 5 || policy.PauseMin != 2*time.Second || policy.PauseMax != 4*time.Second {
		t.Errorf("FanoutPolicy() = %+v, want {5 2s 4s}", policy)
	}
}

// TestNewFromAccountLeavesFanoutOptionsZeroByDefault covers the
// unconfigured case: NewFromAccount leaves core.FanoutPolicy at its zero
// value rather than hardcoding core.Service's own defaults a second
// time — core.Service.Send/Reply already fall back to its built-in
// 10-recipient/3-8s default per zero field (see internal/core's
// fanoutPolicy tests), so duplicating those constants here would risk
// the two drifting apart.
func TestNewFromAccountLeavesFanoutOptionsZeroByDefault(t *testing.T) {
	dir := t.TempDir()
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{"db_path": filepath.Join(dir, "wa.db")},
	}

	adapter, err := NewFromAccount(acc)
	if err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}
	wa := adapter.(*Adapter)
	if policy := wa.FanoutPolicy(); policy != (core.FanoutPolicy{}) {
		t.Errorf("FanoutPolicy() = %+v, want the zero value when unconfigured", policy)
	}
}

func TestStringOption(t *testing.T) {
	opts := map[string]interface{}{"path": "/tmp/x"}
	if got := stringOption(opts, "path", "default"); got != "/tmp/x" {
		t.Errorf("stringOption() = %q, want /tmp/x", got)
	}
	if got := stringOption(opts, "missing", "default"); got != "default" {
		t.Errorf("stringOption() = %q, want default", got)
	}
}

func TestNewFromAccountAppliesHistoryLimitOption(t *testing.T) {
	dir := t.TempDir()
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{
			"db_path":                   filepath.Join(dir, "wa.db"),
			"history_messages_per_chat": int64(9),
		},
	}

	adapter, err := NewFromAccount(acc)
	if err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}
	wa, ok := adapter.(*Adapter)
	if !ok {
		t.Fatalf("adapter is %T, want *Adapter", adapter)
	}
	if wa.historyLimit != 9 {
		t.Errorf("historyLimit = %d, want 9 from config", wa.historyLimit)
	}
	if wa.names == nil {
		t.Errorf("names resolver = nil, want a store-backed resolver wired by NewFromAccount")
	}
}

func TestNewFromAccountDefaultsHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	acc := config.Account{
		Channel: "whatsapp",
		Name:    "personal",
		Options: map[string]interface{}{"db_path": filepath.Join(dir, "wa.db")},
	}

	adapter, err := NewFromAccount(acc)
	if err != nil {
		t.Fatalf("NewFromAccount() error = %v", err)
	}
	wa := adapter.(*Adapter)
	if wa.historyLimit != defaultHistoryMessagesPerChat {
		t.Errorf("historyLimit = %d, want default %d", wa.historyLimit, defaultHistoryMessagesPerChat)
	}
}

func TestIntOption(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]interface{}
		want int
	}{
		{name: "int64", opts: map[string]interface{}{"k": int64(9)}, want: 9},
		{name: "float64", opts: map[string]interface{}{"k": float64(9.7)}, want: 9},
		{name: "missing uses default", opts: map[string]interface{}{}, want: 5},
		{name: "wrong type uses default", opts: map[string]interface{}{"k": "nope"}, want: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := intOption(tc.opts, "k", 5); got != tc.want {
				t.Errorf("intOption() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDurationOptionSeconds(t *testing.T) {
	cases := []struct {
		name string
		opts map[string]interface{}
		want time.Duration
	}{
		{name: "int64 seconds", opts: map[string]interface{}{"k": int64(7)}, want: 7 * time.Second},
		{name: "float64 seconds", opts: map[string]interface{}{"k": float64(2.5)}, want: 2500 * time.Millisecond},
		{name: "missing uses default", opts: map[string]interface{}{}, want: defaultMinSendInterval},
		{name: "wrong type uses default", opts: map[string]interface{}{"k": "nope"}, want: defaultMinSendInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := durationOption(tc.opts, "k", defaultMinSendInterval); got != tc.want {
				t.Errorf("durationOption() = %v, want %v", got, tc.want)
			}
		})
	}
}
