package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// Dos clientes conectados al relay por un mismo board, el delta llega al
// par por TCP (vista "desde fuera" del daemon del otro lado), y el hook del
// daemon se entera del delta criptado y del member.join — el material que
// boardSyncConsumer (cmd/bunker) necesita para aplicar y de lo que el P2P
// Node.OnInbound ya es capaz.
func TestRelayPeerReceivesAndConsumerSeesDelta(t *testing.T) {
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

	// member ana ingresa al relay y recibe los deltas del board.
	ana, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatalf("dial ana: %v", err)
	}
	defer ana.Close()
	join, _ := json.Marshal(RelayMessage{BoardID: "board-e", MemberID: "ana", Type: "member.join"})
	if _, err := ana.Write(append(join, '\n')); err != nil {
		t.Fatalf("join ana: %v", err)
	}

	// member bob ingresa y publica un delta cifrado desde su lado.
	bob, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	defer bob.Close()
	time.Sleep(200 * time.Millisecond) // el registro de ana llegó al relay
	join, _ = json.Marshal(RelayMessage{BoardID: "board-e", MemberID: "bob", Type: "member.join"})
	if _, err := bob.Write(append(join, '\n')); err != nil {
		t.Fatalf("join bob: %v", err)
	}

	delta, _ := json.Marshal(RelayMessage{BoardID: "board-e", MemberID: "bob", Type: "task.create", Payload: []byte(`"contenido-cifrado"`)})
	if _, err := bob.Write(append(delta, '\n')); err != nil {
		t.Fatalf("delta bob: %v", err)
	}

	// La vista de ana: el delta llega por su conexión.
	ana.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(ana).ReadString('\n')
	if err != nil {
		t.Fatalf("el relé no reenvió el delta a ana: %v", err)
	}
	var seen RelayMessage
	if err := json.Unmarshal([]byte(line), &seen); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if seen.Type != "task.create" || string(seen.Payload) != `"contenido-cifrado"` {
		t.Fatalf("nota que recibió ana: %+v", seen)
	}

	// La vista del daemon (el hook del OnMessage): también el delta.
	timeout := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(timeout) {
			t.Fatal("hook no entregó el delta de bob")
		}
		select {
		case msg := <-got:
			if msg.Type == "task.create" && string(msg.Payload) == `"contenido-cifrado"` {
				return
			}
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}
