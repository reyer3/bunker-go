package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sessionFileName is the name of the file a Session is persisted under,
// inside the account's state directory.
const sessionFileName = "session.json"

// Session is the login state bunker-go keeps for one Matrix account: just
// enough to resume a sync without re-authenticating. It is never logged
// and is always written with 0600 permissions.
type Session struct {
	HomeserverURL string `json:"homeserver_url"`
	UserID        string `json:"user_id"`
	AccessToken   string `json:"access_token"`
	DeviceID      string `json:"device_id"`
}

// SaveSession writes session to <dir>/session.json with 0600 permissions,
// creating dir if needed. It overwrites any previous session atomically
// (write to a temp file, then rename) so a crash mid-write never leaves a
// truncated file behind.
func SaveSession(dir string, session Session) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("matrix: create state dir %s: %w", dir, err)
	}

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("matrix: marshal session: %w", err)
	}

	path := filepath.Join(dir, sessionFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("matrix: write session: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("matrix: finalize session: %w", err)
	}
	return nil
}

// LoadSession reads the session persisted at <dir>/session.json. The
// second return value is false, with no error, when no session has been
// saved yet (e.g. before the first login).
func LoadSession(dir string) (Session, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, sessionFileName))
	if os.IsNotExist(err) {
		return Session{}, false, nil
	} else if err != nil {
		return Session{}, false, fmt.Errorf("matrix: read session: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, false, fmt.Errorf("matrix: parse session: %w", err)
	}
	return session, true, nil
}
