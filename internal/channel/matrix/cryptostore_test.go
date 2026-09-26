package matrix

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.mau.fi/util/dbutil"
)

func TestOpenCryptoDatabaseOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crypto.db")

	db, err := OpenCryptoDatabase(path)
	if err != nil {
		t.Fatalf("OpenCryptoDatabase: %v", err)
	}
	t.Cleanup(func() { db.RawDB.Close() })

	if db.Dialect != dbutil.SQLite {
		t.Fatalf("Dialect = %v, want SQLite", db.Dialect)
	}
	if err := db.RawDB.PingContext(context.Background()); err != nil {
		t.Fatalf("ping modernc-backed database: %v", err)
	}
}

// TestOpenCryptoDatabaseCreatesPrivateFileAndDir covers the same
// live-link finding (2026-09-25) as the whatsapp and store packages: the
// crypto database, which holds olm/megolm session state, must never be
// created world- or group-readable, nor its parent directory.
func TestOpenCryptoDatabaseCreatesPrivateFileAndDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode bits are not meaningful on Windows")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "nested")
	path := filepath.Join(dir, "crypto.db")

	db, err := OpenCryptoDatabase(path)
	if err != nil {
		t.Fatalf("OpenCryptoDatabase: %v", err)
	}
	t.Cleanup(func() { db.RawDB.Close() })

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("crypto store dir mode = %o, want 0700", perm)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Errorf("crypto database file mode = %o, want 0600", perm)
	}
}
