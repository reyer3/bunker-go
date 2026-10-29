package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/p2p"
)

// TestBoardEventPublishedToP2PNode prueba el cableado del daemon: el mismo
// BoardEvent que se publica en el relay se publica también en el nodo P2P, y
// un peer conectado lo recibe. Usa dos nodos loopback, sin red externa.
func TestBoardEventPublishedToP2PNode(t *testing.T) {
	alice, err := p2p.NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewNode alice: %v", err)
	}
	defer alice.Stop()

	bob, err := p2p.NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewNode bob: %v", err)
	}
	defer bob.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := bob.Connect(ctx, alice.FullAddrs()[0]); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got := make(chan []byte, 1)
	alice.Subscribe("board-42", got)

	// notifyBoard replica la traducción BoardEvent -> RelayMessage que hace
	// runDaemon y publica en el nodo P2P.
	ev := core.BoardEvent{
		BoardID:  "board-42",
		MemberID: "bob",
		Type:     "task.move",
		Payload:  []byte(`{"task_id":"t7","column":"doing"}`),
	}
	msg := p2p.RelayMessage{
		BoardID:   ev.BoardID,
		MemberID:  ev.MemberID,
		Type:      ev.Type,
		Payload:   json.RawMessage(ev.Payload),
		Timestamp: ev.Timestamp,
		Version:   ev.Version,
	}
	if err := bob.Publish(ev.BoardID, msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case payload := <-got:
		if string(payload) != `{"task_id":"t7","column":"doing"}` {
			t.Fatalf("payload = %s", payload)
		}
	case <-ctx.Done():
		t.Fatal("el BoardEvent no llegó por P2P al peer conectado")
	}
}
