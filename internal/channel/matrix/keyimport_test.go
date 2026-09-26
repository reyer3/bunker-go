package matrix

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/olm"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// newLoggedInAccount writes a Session pointing at srv into a fresh temp
// state dir and returns the config.Account for it, so
// ImportRecoveryKeyForAccount/ImportKeyExportForAccount load it from disk
// exactly like a real "already logged in via SSO" account -- never
// performing a login themselves.
func newLoggedInAccount(t *testing.T, srv *httptest.Server) config.Account {
	t.Helper()
	dir := t.TempDir()
	session := Session{
		HomeserverURL: srv.URL,
		UserID:        "@alice:example.com",
		AccessToken:   "syt_test_token",
		DeviceID:      "DEVICE1",
	}
	if err := SaveSession(dir, session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	return config.Account{
		Channel: "matrix",
		Name:    "work",
		Options: map[string]interface{}{"state_dir": dir},
	}
}

// TestImportRecoveryKeyForAccountUsesExistingSessionAndCryptoStore is the
// wiring proof for T8(a): it never calls SSOLogin or New's login path,
// only LoadSession (an on-disk session.json written ahead of time,
// exactly what a prior `bunker link matrix` run leaves behind) plus a
// real (modernc, pure-Go) on-disk crypto.db, and confirms the recovery
// key import still runs end to end against that existing state.
func TestImportRecoveryKeyForAccountUsesExistingSessionAndCryptoStore(t *testing.T) {
	ssssKey, err := ssss.NewKey("")
	if err != nil {
		t.Fatalf("ssss.NewKey: %v", err)
	}
	recoveryKeyStr := ssssKey.RecoveryKey()

	masterSigning, err := olm.NewPKSigning()
	if err != nil {
		t.Fatalf("olm.NewPKSigning (master): %v", err)
	}
	selfSigning, err := olm.NewPKSigning()
	if err != nil {
		t.Fatalf("olm.NewPKSigning (self): %v", err)
	}
	userSigning, err := olm.NewPKSigning()
	if err != nil {
		t.Fatalf("olm.NewPKSigning (user): %v", err)
	}

	backupKey, err := backup.NewMegolmBackupKey()
	if err != nil {
		t.Fatalf("NewMegolmBackupKey: %v", err)
	}
	encryptedBackupSecret := ssssKey.Encrypt(event.AccountDataMegolmBackupKey.Type, backupKey.Bytes())

	const room = id.RoomID("!backup:matrix.example.org")
	sender := newTestOlmMachine(t, "@alice:matrix.example.org")
	sessionID, encryptedSession := newSingleMegolmSession(t, backupKey, room, sender)

	fake := &fakeCrossSigningHomeserver{
		t: t,
		accountData: map[string]any{
			"m.secret_storage.default_key":                   map[string]string{"key": ssssKey.ID},
			"m.secret_storage.key." + ssssKey.ID:             ssssKey.Metadata,
			string(event.AccountDataCrossSigningMaster.Type): map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: ssssKey.Encrypt(event.AccountDataCrossSigningMaster.Type, masterSigning.Seed())}},
			string(event.AccountDataCrossSigningSelf.Type):   map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: ssssKey.Encrypt(event.AccountDataCrossSigningSelf.Type, selfSigning.Seed())}},
			string(event.AccountDataCrossSigningUser.Type):   map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: ssssKey.Encrypt(event.AccountDataCrossSigningUser.Type, userSigning.Seed())}},
			string(event.AccountDataMegolmBackupKey.Type):    map[string]any{"encrypted": map[string]ssss.EncryptedKeyData{ssssKey.ID: encryptedBackupSecret}},
		},
		keyBackupVersion: mautrix.RespRoomKeysVersion[backup.MegolmAuthData]{
			Algorithm: id.KeyBackupAlgorithmMegolmBackupV1,
			AuthData: backup.MegolmAuthData{
				PublicKey: id.Ed25519(base64.RawStdEncoding.EncodeToString(backupKey.PublicKey().Bytes())),
			},
			Version: "1",
		},
		keyBackupBody: marshalRoomKeysResponse(t, map[id.RoomID]map[id.SessionID]backup.EncryptedSessionData[backup.MegolmSessionData]{
			room: {sessionID: encryptedSession},
		}),
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	acc := newLoggedInAccount(t, srv)

	result, err := ImportRecoveryKeyForAccount(context.Background(), acc, recoveryKeyStr)
	if err != nil {
		t.Fatalf("ImportRecoveryKeyForAccount: %v", err)
	}
	if result.MegolmSessionsRestored != 1 || result.MegolmSessionsTotal != 1 {
		t.Errorf("result = %+v, want 1/1 megolm sessions restored", result)
	}
}

func TestImportRecoveryKeyForAccountErrorsWithoutSavedSession(t *testing.T) {
	acc := config.Account{Channel: "matrix", Name: "work", Options: map[string]interface{}{"state_dir": t.TempDir()}}

	_, err := ImportRecoveryKeyForAccount(context.Background(), acc, "does-not-matter")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("ImportRecoveryKeyForAccount error = %v, want core.ErrUnsupported (no saved session)", err)
	}
}

// TestImportKeyExportForAccountImportsElementExport is the wiring proof
// for T8(d): a real Element-style megolm key export, produced by the
// library's own crypto.ExportKeys (never invented by hand here), gets
// imported into an already-logged-in account's existing crypto store.
func TestImportKeyExportForAccountImportsElementExport(t *testing.T) {
	const room = id.RoomID("!export:matrix.example.org")
	sender := newTestOlmMachine(t, "@alice:matrix.example.org")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	shareContent, ok := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	if !ok {
		t.Fatalf("ShareContent().Parsed is %T, want *event.RoomKeyEventContent", ogs.ShareContent().Parsed)
	}
	senderIdentity := sender.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}

	const passphrase = "correct horse battery staple"
	exportBytes, err := crypto.ExportKeys(passphrase, []*crypto.InboundGroupSession{igs})
	if err != nil {
		t.Fatalf("crypto.ExportKeys: %v", err)
	}

	fake := &fakeCrossSigningHomeserver{t: t, accountData: map[string]any{}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	acc := newLoggedInAccount(t, srv)

	result, err := ImportKeyExportForAccount(context.Background(), acc, passphrase, exportBytes)
	if err != nil {
		t.Fatalf("ImportKeyExportForAccount: %v", err)
	}
	if result.New != 1 || result.AlreadyKnown != 0 || result.Total != 1 {
		t.Errorf("result = %+v, want 1 new, 0 already known, 1 total", result)
	}
}

// TestImportKeyExportForAccountReportsAlreadyKnownOnReimport is the RED
// for T15(a): importing the exact same export twice into the same
// account must report the session as new the first time and already
// known the second, never "imported" both times (mautrix's own
// OlmMachine.ImportKeys counts a session as imported whenever the store
// write succeeds, even when nothing changed -- the live bug this
// reproduces: "imported 159/159" while the crypto store's session count
// never moved).
func TestImportKeyExportForAccountReportsAlreadyKnownOnReimport(t *testing.T) {
	const room = id.RoomID("!export:matrix.example.org")
	sender := newTestOlmMachine(t, "@alice:matrix.example.org")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	shareContent, ok := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	if !ok {
		t.Fatalf("ShareContent().Parsed is %T, want *event.RoomKeyEventContent", ogs.ShareContent().Parsed)
	}
	senderIdentity := sender.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}

	const passphrase = "correct horse battery staple"
	exportBytes, err := crypto.ExportKeys(passphrase, []*crypto.InboundGroupSession{igs})
	if err != nil {
		t.Fatalf("crypto.ExportKeys: %v", err)
	}

	fake := &fakeCrossSigningHomeserver{t: t, accountData: map[string]any{}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	acc := newLoggedInAccount(t, srv)

	first, err := ImportKeyExportForAccount(context.Background(), acc, passphrase, exportBytes)
	if err != nil {
		t.Fatalf("first ImportKeyExportForAccount: %v", err)
	}
	if first.New != 1 || first.AlreadyKnown != 0 || first.Total != 1 {
		t.Fatalf("first import = %+v, want 1 new, 0 already known, 1 total", first)
	}

	second, err := ImportKeyExportForAccount(context.Background(), acc, passphrase, exportBytes)
	if err != nil {
		t.Fatalf("second ImportKeyExportForAccount: %v", err)
	}
	if second.New != 0 || second.AlreadyKnown != 1 || second.Total != 1 {
		t.Fatalf("second import = %+v, want 0 new, 1 already known, 1 total", second)
	}
}

func TestImportKeyExportForAccountRejectsWrongPassphraseWithoutLeakingIt(t *testing.T) {
	const room = id.RoomID("!export:matrix.example.org")
	sender := newTestOlmMachine(t, "@alice:matrix.example.org")

	ogs, err := crypto.NewOutboundGroupSession(room, nil, nil)
	if err != nil {
		t.Fatalf("NewOutboundGroupSession: %v", err)
	}
	ogs.Shared = true
	shareContent := ogs.ShareContent().Parsed.(*event.RoomKeyEventContent)
	senderIdentity := sender.OwnIdentity()
	igs, err := crypto.NewInboundGroupSession(senderIdentity.IdentityKey, senderIdentity.SigningKey, room, shareContent.SessionKey, 0, 0, nil, false)
	if err != nil {
		t.Fatalf("NewInboundGroupSession: %v", err)
	}

	const rightPassphrase = "correct horse battery staple"
	const wrongPassphrase = "wrong passphrase entirely"
	exportBytes, err := crypto.ExportKeys(rightPassphrase, []*crypto.InboundGroupSession{igs})
	if err != nil {
		t.Fatalf("crypto.ExportKeys: %v", err)
	}

	fake := &fakeCrossSigningHomeserver{t: t, accountData: map[string]any{}}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	acc := newLoggedInAccount(t, srv)

	_, err = ImportKeyExportForAccount(context.Background(), acc, wrongPassphrase, exportBytes)
	if err == nil {
		t.Fatal("ImportKeyExportForAccount with the wrong passphrase: want an error")
	}
	if strings.Contains(err.Error(), rightPassphrase) || strings.Contains(err.Error(), wrongPassphrase) {
		t.Errorf("error message %q must never include the passphrase", err.Error())
	}
}
