package p2p

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Request abre un stream al peer, manda el claim y devuelve la respuesta
// que dio su OnRequest: el ida y vuelta que Publish (difusión) no ofrece.
func TestRequestGetsReplyFromPeerHandler(t *testing.T) {
	creator := newTestNode(t)
	joiner := newTestNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := joiner.Connect(ctx, creator.FullAddrs()[0]); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	creator.OnRequest(func(msg RelayMessage) RelayMessage {
		return RelayMessage{BoardID: msg.BoardID, MemberID: msg.MemberID, Type: "member.welcome", Payload: json.RawMessage(`"sellado"`)}
	})

	reply, err := joiner.Request(ctx, creator.ID(), RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.claim", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.Type != "member.welcome" || string(reply.Payload) != `"sellado"` || reply.BoardID != "board-1" {
		t.Fatalf("reply = %+v", reply)
	}
}

// Un nodo sin OnRequest contesta member.reject en vez de colgar al que
// pide: el claim falla ruidoso y se prueba el siguiente peer.
func TestRequestWithoutHandlerIsRejected(t *testing.T) {
	other := newTestNode(t)
	joiner := newTestNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := joiner.Connect(ctx, other.FullAddrs()[0]); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	reply, err := joiner.Request(ctx, other.ID(), RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.claim"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.Type != "member.reject" {
		t.Fatalf("reply = %+v, want member.reject", reply)
	}
	if _, err := joiner.Request(ctx, "no-es-un-peer", RelayMessage{Type: "member.claim"}); err == nil {
		t.Fatal("Request a un peer id inválido no falló")
	}
}
