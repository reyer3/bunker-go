// Package secfile's tests never touch a real user's filesystem outside
// t.TempDir(): every case operates on a throwaway directory the test
// framework creates and removes itself.
package secfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reyer3/bunker-go/internal/secfile"
)

func TestEnsureDirCreatesWithPrivateMode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "nested", "state")

	if err := secfile.EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 0700", perm)
	}
}

func TestEnsureDirTightensExistingLoosePermissions(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "loose")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if err := secfile.EnsureDir(dir); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 0700 after tightening a pre-existing 0755 dir", perm)
	}
}

func TestEnsureFileCreatesWithPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	if err := secfile.EnsureFile(path); err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestEnsureFileTightensExistingLoosePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := secfile.EnsureFile(path); err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600 after tightening a pre-existing 0644 file", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("EnsureFile must not truncate an existing file, got %q", data)
	}
}

func TestSecureSidecarsTightensExistingFilesAndIgnoresMissingOnes(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "state.db")
	wal := base + "-wal"
	if err := os.WriteFile(wal, []byte("wal"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// base+"-shm" is deliberately absent: SecureSidecars must not fail on it.

	secfile.SecureSidecars(base, "-wal", "-shm")

	info, err := os.Stat(wal)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("-wal mode = %o, want 0600", perm)
	}
	if _, err := os.Stat(base + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("-shm should not have been created, Stat err = %v", err)
	}
}
