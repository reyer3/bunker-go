package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestOnMessageDeliversClientMessages: the daemon must receive (and apply)
// the deltas other devices write on the relay. The consumer fires once per
// delta an admitted client writes; the member.join itself is admission,
// not a delta, and never reaches it.
func TestOnMessageDeliversClientMessages(t *testing.T) {
	r := NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	got := make(chan RelayMessage, 4)
	r.OnMessage(func(msg RelayMessage) { got <- msg })

	conn, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	join, _ := json.Marshal(RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.join"})
	if _, err := conn.Write(append(join, '\n')); err != nil {
		t.Fatalf("join write: %v", err)
	}
	delta, _ := json.Marshal(RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "task.create", Payload: []byte(`"cifrado"`)})
	if _, err := conn.Write(append(delta, '\n')); err != nil {
		t.Fatalf("delta write: %v", err)
	}
	_ = bufio.NewReader(conn)

	select {
	case msg := <-got:
		if msg.Type != "task.create" || string(msg.Payload) != `"cifrado"` {
			t.Fatalf("delivery = %+v, want the written delta", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnMessage never fired for client-written messages")
	}
}

// TestOnMessageIgnoresDaemonPublish: the daemon publishes the deltas it
// already persisted via Publish; those must NOT go through the consumer
// (anti-echo: applying them again would create the loop the apply engine
// forbids).
func TestOnMessageIgnoresDaemonPublish(t *testing.T) {
	r := NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	fired := make(chan RelayMessage, 1)
	r.OnMessage(func(msg RelayMessage) { fired <- msg })

	// Nobody to receive (no clients), but Publish must not hit the hook.
	if err := r.Publish(RelayMessage{BoardID: "board-x", Type: "task.create", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case msg := <-fired:
		t.Fatalf("Publish flowed into the consumer hook: %+v", msg)
	case <-time.After(300 * time.Millisecond):
	}
}
