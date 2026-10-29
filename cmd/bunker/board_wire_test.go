package main

import (
	"encoding/json"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
)

// An encrypted delta has to survive the transport's JSON line encoding:
// relay.RelayMessage.Payload (and its P2P twin) is a json.RawMessage, so
// raw ciphertext bytes there make json.Marshal fail and the relay drops the
// message. This pins the full trip: encrypt, encode the line, decode it,
// decrypt.
func TestEncryptedDeltaSurvivesTransportEncoding(t *testing.T) {
	st, boardID := deltaBoardStoreForTests(t)

	plain, _ := json.Marshal(core.Task{Title: "Compras del mes"})
	sent, err := encryptBoardDelta(st, core.BoardEvent{BoardID: boardID, Type: "task.create", Payload: plain})
	if err != nil {
		t.Fatalf("encryptBoardDelta: %v", err)
	}

	line, err := json.Marshal(relay.RelayMessage{BoardID: boardID, Type: "task.create", Payload: sent})
	if err != nil {
		t.Fatalf("encode transport line: %v", err)
	}
	var got relay.RelayMessage
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("decode transport line: %v", err)
	}

	back, err := decryptBoardDelta(st, boardID, got.Payload)
	if err != nil {
		t.Fatalf("decryptBoardDelta: %v", err)
	}
	if string(back) != string(plain) {
		t.Fatalf("round trip = %q, want %q", back, plain)
	}
}
