package matrix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadSession(t *testing.T) {
	dir := t.TempDir()
	want := Session{
		HomeserverURL: "https://matrix.example.org",
		UserID:        "@alice:example.com",
		AccessToken:   "syt_secret_token",
		DeviceID:      "DEVICE1",
	}

	if err := SaveSession(dir, want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, ok, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !ok {
		t.Fatalf("LoadSession: expected a session to be found")
	}
	if got != want {
		t.Fatalf("LoadSession = %+v, want %+v", got, want)
	}
}

func TestSaveSessionPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSession(dir, Session{AccessToken: "secret"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, sessionFileName))
	if err != nil {
		t.Fatalf("stat session file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("session file mode = %o, want 0600", perm)
	}
}

func TestLoadSessionMissing(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession on empty dir: %v", err)
	}
	if ok {
		t.Fatalf("LoadSession: expected no session to be found")
	}
}
