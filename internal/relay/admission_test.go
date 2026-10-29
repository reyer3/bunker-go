package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// admitAll admite cualquier claim o join y responde el claim con un
// welcome: los tests de enrutado no prueban la admisión.
func admitAll(msg RelayMessage) (*RelayMessage, error) {
	if msg.Type == "member.claim" {
		return &RelayMessage{BoardID: msg.BoardID, MemberID: msg.MemberID, Type: "member.welcome", Payload: json.RawMessage(`"ok"`)}, nil
	}
	return nil, nil
}

func startRelay(t *testing.T) *Relay {
	t.Helper()
	r := NewRelay("127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })
	return r
}

type rawClient struct {
	conn net.Conn
	rd   *bufio.Reader
}

func dialRelay(t *testing.T, r *Relay) *rawClient {
	t.Helper()
	conn, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &rawClient{conn: conn, rd: bufio.NewReader(conn)}
}

func (c *rawClient) write(t *testing.T, msg RelayMessage) {
	t.Helper()
	line, _ := json.Marshal(msg)
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *rawClient) read(wait time.Duration) (RelayMessage, bool) {
	c.conn.SetReadDeadline(time.Now().Add(wait))
	defer c.conn.SetReadDeadline(time.Time{})
	line, err := c.rd.ReadString('\n')
	if err != nil {
		return RelayMessage{}, false
	}
	var msg RelayMessage
	if json.Unmarshal([]byte(line), &msg) != nil {
		return RelayMessage{}, false
	}
	return msg, true
}

// Sin admisión configurada el relay no registra a nadie: un member.join
// sin prueba es exactamente el hueco que la admisión cierra.
func TestRelayWithoutAdmissionRejectsJoin(t *testing.T) {
	r := startRelay(t)
	c := dialRelay(t, r)
	c.write(t, RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.join"})
	reply, ok := c.read(2 * time.Second)
	if !ok || reply.Type != "member.reject" {
		t.Fatalf("reply = %+v, ok = %v; want member.reject", reply, ok)
	}
	_ = r.Publish(RelayMessage{BoardID: "board-1", Type: "task.create", Payload: json.RawMessage(`{}`)})
	if msg, ok := c.read(300 * time.Millisecond); ok {
		t.Fatalf("un cliente no admitido recibió %+v", msg)
	}
}

// Un claim rechazado no registra al cliente y le dice por qué; uno
// confirmado le entrega el welcome y desde ahí recibe los deltas del board.
func TestRelayClaimRegistersOnlyAfterConfirmation(t *testing.T) {
	r := startRelay(t)
	r.SetAdmission(func(msg RelayMessage) (*RelayMessage, error) {
		if string(msg.Payload) != `"buena"` {
			return nil, errors.New("prueba inválida")
		}
		return &RelayMessage{BoardID: msg.BoardID, MemberID: msg.MemberID, Type: "member.welcome", Payload: json.RawMessage(`"sellado"`)}, nil
	})
	c := dialRelay(t, r)

	c.write(t, RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.claim", Payload: json.RawMessage(`"mala"`)})
	reply, ok := c.read(2 * time.Second)
	if !ok || reply.Type != "member.reject" || !strings.Contains(string(reply.Payload), "prueba inválida") {
		t.Fatalf("reply = %+v; want member.reject con el motivo", reply)
	}
	_ = r.Publish(RelayMessage{BoardID: "board-1", Type: "task.create", Payload: json.RawMessage(`{}`)})
	if msg, ok := c.read(300 * time.Millisecond); ok {
		t.Fatalf("un claim rechazado recibió %+v", msg)
	}

	c.write(t, RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.claim", Payload: json.RawMessage(`"buena"`)})
	reply, ok = c.read(2 * time.Second)
	if !ok || reply.Type != "member.welcome" || string(reply.Payload) != `"sellado"` {
		t.Fatalf("reply = %+v; want el welcome", reply)
	}
	_ = r.Publish(RelayMessage{BoardID: "board-1", Type: "task.create", Payload: json.RawMessage(`{"n":1}`)})
	if msg, ok := c.read(2 * time.Second); !ok || msg.Type != "task.create" {
		t.Fatalf("el cliente admitido no recibió el delta: %+v %v", msg, ok)
	}
}

// Un cliente no admitido no puede inyectar deltas en el daemon: el
// consumidor (apply del store local) solo ve lo que escriben los admitidos.
func TestRelayConsumerIgnoresUnadmittedClients(t *testing.T) {
	r := startRelay(t)
	got := make(chan RelayMessage, 4)
	r.OnMessage(func(msg RelayMessage) { got <- msg })
	c := dialRelay(t, r)
	c.write(t, RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "task.create", Payload: json.RawMessage(`{}`)})
	select {
	case msg := <-got:
		t.Fatalf("el consumidor recibió un delta de un cliente no admitido: %+v", msg)
	case <-time.After(300 * time.Millisecond):
	}
}

// Claim es el cliente del claim: manda member.claim y devuelve la
// respuesta del relay, sea welcome o rechazo.
func TestClaimClientReturnsReply(t *testing.T) {
	r := startRelay(t)
	r.SetAdmission(admitAll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply, err := Claim(ctx, r.Addr().String(), RelayMessage{BoardID: "board-1", MemberID: "m1", Type: "member.claim", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if reply.Type != "member.welcome" {
		t.Fatalf("reply = %+v", reply)
	}
	if _, err := Claim(ctx, "127.0.0.1:1", RelayMessage{Type: "member.claim"}); err == nil {
		t.Fatal("Claim contra un relay caído no falló")
	}
}

// Stop cierra también las conexiones de clientes nunca admitidos: si no,
// esperaría para siempre a sus lectores.
func TestRelayStopClosesUnadmittedClients(t *testing.T) {
	r := NewRelay("127.0.0.1:0")
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(100 * time.Millisecond) // el relay aceptó la conexión
	done := make(chan struct{})
	go func() { _ = r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop quedó esperando a un cliente no admitido")
	}
}
