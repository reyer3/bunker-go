package p2p

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestOnInboundDeliversRemoteStreams: como payload solo viaja el delta, el
// daemon necesita el mensaje completo (board, miembro, tipo) para analizar
// y aplicar. El hook OnInbound entrega cada mensaje que llega por stream de
// un peer; los mensajes publicados por este nodo no pasan por ahí.
func TestOnInboundDeliversRemoteStreams(t *testing.T) {
	alice, err := NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewNode alice: %v", err)
	}
	defer alice.Stop()
	bob, err := NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewNode bob: %v", err)
	}
	defer bob.Stop()

	got := make(chan RelayMessage, 4)
	alice.OnInbound(func(msg RelayMessage) { got <- msg })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := bob.Connect(ctx, alice.FullAddrs()[0]); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := bob.Publish("board-42", RelayMessage{
		BoardID:  "board-42",
		MemberID: "bob",
		Type:     "task.create",
		Payload:  []byte(`"cifrado"`),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case msg := <-got:
		if msg.Type != "task.create" || msg.BoardID != "board-42" || string(msg.Payload) != `"cifrado"` {
			b, _ := json.Marshal(msg)
			t.Fatalf("OnInbound = %s", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnInbound nunca entregó el stream entrante")
	}
}
