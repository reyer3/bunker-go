package matrix

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

// openCryptoHelper builds the client and crypto helper for an
// already-logged-in account, using the SAME session and crypto store New
// wires for the daemon, and initializes it (helper.Init): enough to
// obtain a *crypto.OlmMachine (helper.Machine()) for one-shot key-import
// operations, without ever starting the /sync loop. It never logs in
// again: a missing session is an error naming the account, exactly like
// New.
//
// The caller must call helper.Close() when done, to release the crypto
// database.
func openCryptoHelper(ctx context.Context, acc config.Account) (*cryptohelper.CryptoHelper, error) {
	stateDir := StateDirFor(acc)

	session, ok, err := LoadSession(stateDir)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("matrix: account %q has no saved session; run the SSO login flow first: %w", acc.Name, core.ErrUnsupported)
	}

	client, err := mautrix.NewClient(session.HomeserverURL, id.UserID(session.UserID), session.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("matrix: create client: %w", err)
	}
	client.DeviceID = id.DeviceID(session.DeviceID)
	// cryptohelper.NewCryptoHelper requires an ExtensibleSyncer; Init
	// registers its own handlers on it, but this helper never calls
	// client.SyncWithContext, so nothing is ever dispatched through it.
	client.Syncer = mautrix.NewDefaultSyncer()

	db, err := OpenCryptoDatabase(filepath.Join(stateDir, "crypto.db"))
	if err != nil {
		return nil, err
	}
	pickleKey, err := loadOrCreatePickleKey(stateDir)
	if err != nil {
		db.Close()
		return nil, err
	}
	helper, err := cryptohelper.NewCryptoHelper(client, pickleKey, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("matrix: set up crypto helper: %w", err)
	}
	if err := helper.Init(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("matrix: init crypto: %w", err)
	}
	return helper, nil
}

// ImportKeyResult reports how a megolm key export was applied to an
// account's crypto store. mautrix crypto's own OlmMachine.ImportKeys
// counts a session as "imported" whenever storing it succeeds, even when
// the store already had that exact session (storing is an idempotent
// upsert) -- which is what produced the misleading live "imported
// 159/159 megolm sessions" while the crypto store's session count never
// moved. New and AlreadyKnown split that count honestly; Failed is
// exported sessions the machine rejected outright (bad algorithm, ID
// mismatch) before ever touching the store. New + AlreadyKnown + Failed
// == Total.
type ImportKeyResult struct {
	New          int
	AlreadyKnown int
	Failed       int
	Total        int
}

// trackingCryptoStore wraps a crypto.Store and observes the
// GetGroupSession calls OlmMachine.StoreGroupSession makes internally:
// it always looks up the exact (RoomID, SessionID) pair first to decide
// whether to insert or update. That lookup is the only place the machine
// itself distinguishes "already have this session" from "new session",
// so wrapping it here gets an accurate per-session count without bunker
// reimplementing the key-export decryption format (decodeKeyExport and
// decryptKeyExport are unexported in maunium.net/go/mautrix/crypto).
type trackingCryptoStore struct {
	crypto.Store

	mu           sync.Mutex
	newSessions  int
	alreadyKnown int
}

func (s *trackingCryptoStore) GetGroupSession(ctx context.Context, roomID id.RoomID, sessionID id.SessionID) (*crypto.InboundGroupSession, error) {
	sess, err := s.Store.GetGroupSession(ctx, roomID, sessionID)
	if err != nil && !errors.Is(err, crypto.ErrGroupSessionWithheld) {
		return sess, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess != nil {
		s.alreadyKnown++
	} else {
		s.newSessions++
	}
	return sess, err
}

// ImportKeyExportForAccount imports an Element-style megolm key export
// ("-----BEGIN MEGOLM SESSION DATA-----...") into an already-logged-in
// account's existing crypto store, using mautrix crypto's own
// OlmMachine.ImportKeys, and reports new-versus-already-known session
// counts (see ImportKeyResult).
func ImportKeyExportForAccount(ctx context.Context, acc config.Account, passphrase string, data []byte) (ImportKeyResult, error) {
	helper, err := openCryptoHelper(ctx, acc)
	if err != nil {
		return ImportKeyResult{}, err
	}
	defer helper.Close()

	machine := helper.Machine()
	tracker := &trackingCryptoStore{Store: machine.CryptoStore}
	machine.CryptoStore = tracker

	_, total, err := machine.ImportKeys(ctx, passphrase, data)
	if err != nil {
		return ImportKeyResult{}, fmt.Errorf("matrix: import key export: %w", err)
	}

	result := ImportKeyResult{
		New:          tracker.newSessions,
		AlreadyKnown: tracker.alreadyKnown,
		Total:        total,
	}
	result.Failed = total - result.New - result.AlreadyKnown
	return result, nil
}
