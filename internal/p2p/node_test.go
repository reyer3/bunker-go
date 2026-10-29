package p2p

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// newTestNode crea un nodo escuchando solo en loopback con puerto efímero.
func newTestNode(t *testing.T) *Node {
	t.Helper()
	n, err := NewNode("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop() })
	return n
}

// TestPublishDeliversToConnectedPeer prueba el camino completo: dos nodos se
// conectan y un mensaje publicado en uno llega al suscriptor del otro.
func TestPublishDeliversToConnectedPeer(t *testing.T) {
	alice := newTestNode(t)
	bob := newTestNode(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := bob.Connect(ctx, alice.FullAddrs()[0]); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	got := make(chan []byte, 1)
	alice.Subscribe("board-1", got)

	payload := json.RawMessage(`{"task_id":"t1","column":"done"}`)
	msg := RelayMessage{
		BoardID:  "board-1",
		MemberID: "bob",
		Type:     "task.done",
		Payload:  payload,
	}
	if err := bob.Publish("board-1", msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case received := <-got:
		if string(received) != string(payload) {
			t.Fatalf("payload = %s, want %s", received, payload)
		}
	case <-ctx.Done():
		t.Fatal("el mensaje no llegó al peer conectado")
	}
}

// TestPublishDeliversLocally prueba que un nodo entrega a sus propios
// suscriptores sin necesitar peers conectados.
func TestPublishDeliversLocally(t *testing.T) {
	n := newTestNode(t)

	got := make(chan []byte, 1)
	n.Subscribe("board-local", got)

	msg := RelayMessage{BoardID: "board-local", Type: "task.create", Payload: json.RawMessage(`{"task_id":"t2"}`)}
	if err := n.Publish("board-local", msg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case received := <-got:
		if string(received) != `{"task_id":"t2"}` {
			t.Fatalf("payload = %s", received)
		}
	default:
		t.Fatal("el suscriptor local no recibió el mensaje")
	}
}

// TestPeerInfoFromMultiaddrRejectsInvalid prueba que una multiaddr sin peer id
// se rechaza con error en vez de conectarse a ciegas.
func TestPeerInfoFromMultiaddrRejectsInvalid(t *testing.T) {
	if _, err := PeerInfoFromMultiaddr("/ip4/127.0.0.1/tcp/4001"); err == nil {
		t.Fatal("esperaba error para multiaddr sin peer id")
	}
	if _, err := PeerInfoFromMultiaddr("no-es-una-multiaddr"); err == nil {
		t.Fatal("esperaba error para multiaddr inválida")
	}
}
