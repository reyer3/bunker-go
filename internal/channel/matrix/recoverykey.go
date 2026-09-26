package matrix

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"

	"github.com/reyer3/bunker-go/internal/config"
)

// RecoveryKeyResult reports what ImportRecoveryKey restored from the
// server-side megolm key backup, so a caller (the CLI) can print
// "restored N/M megolm sessions" instead of a silent success. It says
// nothing about the cross-signing half: that step is all-or-nothing (a
// non-nil error from ImportRecoveryKey means neither half completed).
type RecoveryKeyResult struct {
	MegolmSessionsRestored int
	MegolmSessionsTotal    int
}

// ImportRecoveryKey restores an account's cross-signing keys (via secret
// storage, SSSS) and its megolm sessions (via the server-side key backup)
// from a single recovery key -- the "security key" Element shows on
// setup. It is never run against a real account in tests: mach.Client
// dials whatever homeserver it was built with, which in every test here
// is an httptest fake, never matrix.example.org.
func ImportRecoveryKey(ctx context.Context, mach *crypto.OlmMachine, recoveryKey string) (RecoveryKeyResult, error) {
	if err := mach.VerifyWithRecoveryKey(ctx, recoveryKey); err != nil {
		return RecoveryKeyResult{}, fmt.Errorf("matrix: restore cross-signing keys: %w", err)
	}

	backupKey, err := fetchMegolmBackupKey(ctx, mach, recoveryKey)
	if err != nil {
		return RecoveryKeyResult{}, fmt.Errorf("matrix: fetch megolm backup key: %w", err)
	}

	result, err := restoreMegolmSessionsFromBackup(ctx, mach, backupKey)
	if err != nil {
		return RecoveryKeyResult{}, fmt.Errorf("matrix: restore key backup: %w", err)
	}
	return result, nil
}

// restoreMegolmSessionsFromBackup downloads the latest server-side key
// backup and imports every session it can decrypt. Unlike
// mach.DownloadAndStoreLatestKeyBackup (which reports nothing back), this
// counts successes and failures itself -- by inlining
// mach.GetAndStoreKeyBackup's loop -- so the caller can report real
// numbers instead of a bare "ok".
func restoreMegolmSessionsFromBackup(ctx context.Context, mach *crypto.OlmMachine, backupKey *backup.MegolmBackupKey) (RecoveryKeyResult, error) {
	versionInfo, err := mach.GetAndVerifyLatestKeyBackupVersion(ctx, backupKey)
	if err != nil {
		return RecoveryKeyResult{}, fmt.Errorf("verify key backup version: %w", err)
	}
	if versionInfo == nil {
		// No key backup exists on the server at all: not an error, just
		// nothing to restore.
		return RecoveryKeyResult{}, nil
	}

	keys, err := mach.Client.GetKeyBackup(ctx, versionInfo.Version)
	if err != nil {
		return RecoveryKeyResult{}, fmt.Errorf("fetch key backup: %w", err)
	}

	var result RecoveryKeyResult
	for roomID, roomBackup := range keys.Rooms {
		for sessionID, keyBackupData := range roomBackup.Sessions {
			result.MegolmSessionsTotal++
			sessionData, err := keyBackupData.SessionData.Decrypt(backupKey)
			if err != nil {
				continue
			}
			if _, err := mach.ImportRoomKeyFromBackup(ctx, versionInfo.Version, roomID, sessionID, sessionData); err != nil {
				continue
			}
			result.MegolmSessionsRestored++
		}
	}
	return result, nil
}

// ImportRecoveryKeyForAccount loads an already-logged-in account's
// existing session and crypto store (never logging in again) and imports
// recoveryKey into it via ImportRecoveryKey. This is the entry point
// `bunker link matrix <acct> --recovery-key[-stdin]` calls.
func ImportRecoveryKeyForAccount(ctx context.Context, acc config.Account, recoveryKey string) (RecoveryKeyResult, error) {
	helper, err := openCryptoHelper(ctx, acc)
	if err != nil {
		return RecoveryKeyResult{}, err
	}
	defer helper.Close()

	return ImportRecoveryKey(ctx, helper.Machine(), recoveryKey)
}

// fetchMegolmBackupKey retrieves and decrypts the m.megolm_backup.v1
// secret from SSSS using recoveryKey, returning the server key backup's
// private key.
func fetchMegolmBackupKey(ctx context.Context, mach *crypto.OlmMachine, recoveryKey string) (*backup.MegolmBackupKey, error) {
	keyID, keyData, err := mach.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return nil, fmt.Errorf("get default SSSS key: %w", err)
	}
	key, err := keyData.VerifyRecoveryKey(keyID, recoveryKey)
	if err != nil && !errors.Is(err, ssss.ErrUnverifiableKey) {
		return nil, fmt.Errorf("verify recovery key: %w", err)
	}

	raw, err := mach.SSSS.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt megolm backup secret: %w", err)
	}

	backupKey, err := backup.MegolmBackupKeyFromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse megolm backup key: %w", err)
	}
	return backupKey, nil
}
