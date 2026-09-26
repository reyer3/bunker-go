package matrix

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"go.mau.fi/util/dbutil"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver, no CGO

	"github.com/reyer3/bunker-go/internal/secfile"
)

// sqliteDriverName is the driver modernc.org/sqlite registers itself
// under (sql.Register("sqlite", ...)). It is also a valid dbutil dialect
// string: dbutil.ParseDialect accepts anything with a "sqlite" prefix.
const sqliteDriverName = "sqlite"

// OpenCryptoDatabase opens (creating if needed) the pure-Go SQLite
// database at path and wraps it as a *dbutil.Database, the type
// mautrix's crypto/cryptohelper.NewCryptoHelper expects when it should
// manage its own crypto/state stores. Using modernc.org/sqlite here
// (instead of a dbutil dialect string like "sqlite3-fk-wal", which
// resolves to the CGO-based mattn/go-sqlite3 driver) keeps the whole
// adapter CGO-free.
func OpenCryptoDatabase(path string) (*dbutil.Database, error) {
	// The crypto database holds olm/megolm session state, so it is
	// created (or tightened) private before the driver ever touches it:
	// see internal/secfile for why a DSN string alone cannot do this.
	if dir := filepath.Dir(path); dir != "." {
		if err := secfile.EnsureDir(dir); err != nil {
			return nil, fmt.Errorf("matrix: create state dir: %w", err)
		}
	}
	if err := secfile.EnsureFile(path); err != nil {
		return nil, fmt.Errorf("matrix: create private crypto database %s: %w", path, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", path)
	rawDB, err := sql.Open(sqliteDriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("matrix: open crypto database %s: %w", path, err)
	}
	rawDB.SetMaxOpenConns(1) // modernc.org/sqlite: serialize writers, WAL still allows concurrent readers.

	db, err := dbutil.NewWithDB(rawDB, sqliteDriverName)
	if err != nil {
		rawDB.Close()
		return nil, fmt.Errorf("matrix: wrap crypto database: %w", err)
	}
	secfile.SecureSidecars(path, "-wal", "-shm")
	return db, nil
}
