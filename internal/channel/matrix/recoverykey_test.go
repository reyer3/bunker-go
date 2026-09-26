package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/id"
)

// newAccountDataHomeserver serves GET .../account_data/<name> from a
// prepared map, exactly what ssss.Machine needs to fetch secret storage
// key metadata and encrypted secrets.
//
// The account data "name" is one opaque string that may itself contain
// "/" (SSSS key IDs are unpadded base64 of 24 random bytes, so about 40%
// of them do): it must be taken as everything after "/account_data/",
// never via path.Base, which would wrongly split on an embedded "/".
func newAccountDataHomeserver(t *testing.T, accountData map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const marker = "/account_data/"
		idx := strings.Index(r.URL.Path, marker)
		if idx < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		name := r.URL.Path[idx+len(marker):]
		value, ok := accountData[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"errcode": "M_NOT_FOUND"})
			return
		}
		json.NewEncoder(w).Encode(value)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchMegolmBackupKeyDecryptsFromSSSS(t *testing.T) {
	ssssKey, err := ssss.NewKey("")
	if err != nil {
		t.Fatalf("ssss.NewKey: %v", err)
	}
	recoveryKeyStr := ssssKey.RecoveryKey()

	wantBackupKey, err := backup.NewMegolmBackupKey()
	if err != nil {
		t.Fatalf("backup.NewMegolmBackupKey: %v", err)
	}
	encryptedBackupSecret := ssssKey.Encrypt("m.megolm_backup.v1", wantBackupKey.Bytes())

	srv := newAccountDataHomeserver(t, map[string]any{
		"m.secret_storage.default_key":       map[string]string{"key": ssssKey.ID},
		"m.secret_storage.key." + ssssKey.ID: ssssKey.Metadata,
		"m.megolm_backup.v1":                 map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: encryptedBackupSecret}},
	})

	client, err := mautrix.NewClient(srv.URL, id.UserID("@alice:example.com"), "syt_test_token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	mach := newTestOlmMachine(t, "@alice:example.com")
	mach.Client = client
	mach.SSSS = ssss.NewSSSSMachine(client)

	gotBackupKey, err := fetchMegolmBackupKey(context.Background(), mach, recoveryKeyStr)
	if err != nil {
		t.Fatalf("fetchMegolmBackupKey: %v", err)
	}

	if string(gotBackupKey.Bytes()) != string(wantBackupKey.Bytes()) {
		t.Errorf("recovered backup key bytes do not match the one stored in SSSS")
	}
	if string(gotBackupKey.PublicKey().Bytes()) != string(wantBackupKey.PublicKey().Bytes()) {
		t.Errorf("recovered backup key public key does not match")
	}
}

func TestFetchMegolmBackupKeyRejectsWrongRecoveryKey(t *testing.T) {
	ssssKey, err := ssss.NewKey("")
	if err != nil {
		t.Fatalf("ssss.NewKey: %v", err)
	}
	backupKey, err := backup.NewMegolmBackupKey()
	if err != nil {
		t.Fatalf("backup.NewMegolmBackupKey: %v", err)
	}
	encryptedBackupSecret := ssssKey.Encrypt("m.megolm_backup.v1", backupKey.Bytes())

	srv := newAccountDataHomeserver(t, map[string]any{
		"m.secret_storage.default_key":       map[string]string{"key": ssssKey.ID},
		"m.secret_storage.key." + ssssKey.ID: ssssKey.Metadata,
		"m.megolm_backup.v1":                 map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: encryptedBackupSecret}},
	})

	client, err := mautrix.NewClient(srv.URL, id.UserID("@alice:example.com"), "syt_test_token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	mach := newTestOlmMachine(t, "@alice:example.com")
	mach.Client = client
	mach.SSSS = ssss.NewSSSSMachine(client)

	wrongKey, err := ssss.NewKey("")
	if err != nil {
		t.Fatalf("ssss.NewKey: %v", err)
	}

	if _, err := fetchMegolmBackupKey(context.Background(), mach, wrongKey.RecoveryKey()); err == nil {
		t.Fatal("fetchMegolmBackupKey: expected an error for a recovery key that does not match the stored SSSS key")
	}
}
