package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/reyer3/bunker-go/internal/board"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// remoteBoardForSync abre un store, crea un board con su clave simétrica y
// una columna canónica, como el daemon real lo deja montado al partir.
func remoteBoardForSync(t *testing.T) (*store.Store, *core.Service, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	boardSvcLocal := core.NewBoardService(st)
	b, err := boardSvcLocal.CreateBoard(t.Context(), "Tablero", "member-ana")
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if err := st.CreateColumn(&core.Column{ID: "col-1", Board: b.ID, Name: "Por hacer", Order: 0, Color: "#4db0ff"}); err != nil {
		t.Fatalf("CreateColumn: %v", err)
	}
	svc := core.NewService(st, core.NewRegistry())
	return st, svc, b.ID
}

// encryptedDelta cifra un payload con la clave del board, como el
// dispositivo emisor lo habría dejado en el transporte.
func encryptedDelta(t *testing.T, st *store.Store, boardID, typ string, payload []byte) []byte {
	t.Helper()
	row, err := st.GetBoard(boardID)
	if err != nil {
		t.Fatalf("GetBoard: %v", err)
	}
	crypto, err := board.NewBoardCrypto()
	if err != nil {
		t.Fatal(err)
	}
	crypto.SetPeerKey(row.EncryptionKey)
	enc, err := crypto.Encrypt(payload)
	if err != nil {
		t.Fatal(err)
	}
	// On the wire the ciphertext is a base64 JSON string (encryptBoardDelta).
	wire, err := json.Marshal(enc)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

// El consumidor del daemon (lo que conecta a Relay.OnMessage y Node.
// OnInbound) debe descifrar el delta con la clave local del board y
// aplicarlo en el store: una tarea creada en otro dispositivo aparece en
// la lista del board de este uno sin acciones manuales.
func TestBoardSyncConsumerAppliesEncryptedDelta(t *testing.T) {
	st, svc, boardID := remoteBoardForSync(t)

	task := core.Task{ID: "task-remoto-1", BoardID: boardID, ColumnID: "col-1", Title: "Segundo CPU", CreatedBy: "member-bob", Version: 0}
	payload, _ := json.Marshal(task)
	var stderr bytes.Buffer
	consumer := boardSyncConsumer(st, svc, &stderr)
	consumer(boardID, "member-bob", "task.create", encryptedDelta(t, st, boardID, "task.create", payload), 0, 0)

	stats, err := svc.BoardTasks(context.Background(), boardID)
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	if len(stats) != 1 || stats[0].Title != "Segundo CPU" {
		t.Fatalf("tasks = %+v, want remote-applied task", stats)
	}
}

// member.join y tipos desconocidos no aplican nada: solo los deltas de
// tablero (creación, movimiento, completado, comentario) son aplicables.
func TestBoardSyncConsumerIgnoresJoinsAndUnknownTypes(t *testing.T) {
	st, svc, boardID := remoteBoardForSync(t)

	var stderr bytes.Buffer
	consumer := boardSyncConsumer(st, svc, &stderr)
	consumer(boardID, "member-bob", "member.join", nil, 0, 0)
	consumer(boardID, "member-bob", "mirar.board", []byte(`{}`), 0, 0)

	stats, err := svc.BoardTasks(context.Background(), boardID)
	if err != nil {
		t.Fatalf("BoardTasks: %v", err)
	}
	if len(stats) != 0 {
		t.Fatalf("se aplicó un tipo no-delta: %+v", stats)
	}
}
