package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/board"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// deltaBoardStoreForTests opens a store and creates a board through the
// service, so its symmetric key lands in the store exactly like the daemon
// runs it. Returns both the store and the board id.
func deltaBoardStoreForTests(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := core.NewBoardService(st)
	b, err := svc.CreateBoard(t.Context(), "Tablero", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	return st, b.ID
}

// encryptBoardDelta is the transport boundary the daemon publishes through
// (cmd/bunker/daemon.go notifyBoard): what travels over the relay and the
// P2P peers is the task's content, so it must leave encrypted with the
// board's symmetric key. The key never leaves the store, so the transport
// sees ciphertext only.
func TestEncryptBoardDelta(t *testing.T) {
	st, boardID := deltaBoardStoreForTests(t)

	plain, _ := json.Marshal(core.Task{Title: "Compras del mes"})
	ev := core.BoardEvent{BoardID: boardID, Type: "task.create", Payload: plain}
	got, err := encryptBoardDelta(st, ev)
	if err != nil {
		t.Fatalf("encryptBoardDelta: %v", err)
	}
	if strings.Contains(string(got), "Compras") {
		t.Fatalf("payload travels in plaintext over the transport: %q", got)
	}

	row, err := st.GetBoard(boardID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	crypto, err := board.NewBoardCrypto()
	if err != nil {
		t.Fatal(err)
	}
	crypto.SetPeerKey(row.EncryptionKey)
	back, err := crypto.Decrypt(got)
	if err != nil {
		t.Fatalf("a member holding the board key could not decrypt the delta: %v", err)
	}
	if string(back) != string(plain) {
		t.Fatalf("decrypted delta = %q, want %q", back, plain)
	}
}

// TestEncryptBoardDeltaWithoutKeyPassesThrough: a board without an
// encryption key never promises E2EE, so the delta leaves as-was rather
// than breaking sync for boards the user left unencrypted.
func TestEncryptBoardDeltaWithoutKeyPassesThrough(t *testing.T) {
	st, _ := deltaBoardStoreForTests(t)
	// Un board creado sin cifrado: EncryptionKey nil.
	row := &core.Board{ID: "board-clear", Name: "sin cifrado", OwnerID: "member-bob"}
	if err := st.CreateBoard(row); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	raw := []byte(`{"title":"sin cifrar"}`)
	ev := core.BoardEvent{BoardID: row.ID, Type: "task.create", Payload: raw}
	got, err := encryptBoardDelta(st, ev)
	if !errors.Is(err, errNoBoardKey) {
		t.Fatalf("err = %v, want errNoBoardKey for a keyless board", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("payload = %q, want passthrough for a keyless board", got)
	}
}
