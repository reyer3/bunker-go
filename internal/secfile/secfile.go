// Package secfile creates the private files and directories bunker-go's
// state lives in. Every store, whatsmeow device database and Matrix
// crypto database it opens holds secrets (message content, session
// keys), so its directory must be 0700 and its files 0600 - never
// whatever the process's default umask happens to leave behind, which on
// most Linux setups is 0644/0755.
package secfile

import "os"

// EnsureDir makes sure dir (and any missing parents) exists with mode
// 0700, tightening an already-existing directory's mode too: a directory
// created by an older build before this fix, or one whose mode drifted
// for any other reason, is fixed the next time it is opened rather than
// left loose forever.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// EnsureFile makes sure a file exists at path with mode 0600. When path
// does not exist yet, it is created empty with that mode directly -
// important for SQLite drivers (modernc.org/sqlite here) that open a
// path via a DSN string and give callers no way to pass a mode: by the
// time the driver opens it, the file already exists and is private, so
// it is never created fresh under the process's own (looser) umask. An
// existing file's mode is tightened the same way, without touching its
// contents.
func EnsureFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// SecureSidecars tightens base+suffix to 0600 for every suffix whose
// file already exists, e.g. SQLite's "-wal" and "-shm" journal files.
// modernc.org/sqlite creates those itself, on its own schedule (the
// first write after WAL mode is enabled), with no hook this package can
// use to set their mode at creation; calling this again after that
// point is the only way to catch them. A suffix that does not exist yet
// (WAL mode enabled but no write has happened) is silently skipped, not
// an error - callers that write immediately after opening, as this
// package's callers do, close that gap in practice, but a sidecar
// created still later under the process's default umask needs another
// call to be caught.
func SecureSidecars(base string, suffixes ...string) {
	for _, suf := range suffixes {
		_ = os.Chmod(base+suf, 0o600) // ENOENT is expected when the sidecar has not been created yet.
	}
}
