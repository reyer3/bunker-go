package relay_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/relay"
)

const (
	testTimeout  = 5 * time.Second
	shortTimeout = 200 * time.Millisecond
)

// testClient es un cliente de prueba que se conecta al relay por TCP.
type testClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

func dial(t *testing.T, addr string) *testClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return &testClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}
}

func (c *testClient) close() {
	_ = c.conn.Close()
}

func (c *testClient) send(t *testing.T, msg relay.RelayMessage) {
	t.Helper()
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	line = append(line, '\n')
	if _, err := c.conn.Write(line); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *testClient) setReadDeadline(d time.Duration) {
	c.conn.SetReadDeadline(time.Now().Add(d))
}

func (c *testClient) clearReadDeadline() {
	c.conn.SetReadDeadline(time.Time{})
}

// receive espera un mensaje hasta testTimeout y falla la prueba si no llega.
func (c *testClient) receive(t *testing.T) *relay.RelayMessage {
	t.Helper()
	c.setReadDeadline(testTimeout)
	defer c.clearReadDeadline()
	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg relay.RelayMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &msg
}

// receiveOrTimeout espera un mensaje hasta shortTimeout. Devuelve ok=false si
// no llega nada (útil para comprobar aislamiento entre boards).
func (c *testClient) receiveOrTimeout() (*relay.RelayMessage, bool) {
	c.setReadDeadline(shortTimeout)
	defer c.clearReadDeadline()
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return nil, false
	}
	var msg relay.RelayMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, false
	}
	return &msg, true
}

// join envía un member.join al board dado.
func (c *testClient) join(t *testing.T, boardID, memberID string) {
	t.Helper()
	c.send(t, relay.RelayMessage{
		BoardID:  boardID,
		MemberID: memberID,
		Type:     "member.join",
	})
}

// waitForJoin da tiempo al relay para registrar los clientes conectados.
func waitForJoin() {
	time.Sleep(100 * time.Millisecond)
}

func TestRelayBroadcastSameBoard(t *testing.T) {
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllExt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	addr := r.Addr().String()

	// Cliente 1 se une al board A.
	c1 := dial(t, addr)
	t.Cleanup(c1.close)
	c1.join(t, "board-a", "member-1")

	// Cliente 2 se une al mismo board A.
	c2 := dial(t, addr)
	t.Cleanup(c2.close)
	c2.join(t, "board-a", "member-2")

	waitForJoin()

	// Cliente 1 envía un delta de tarea.
	payload := []byte(`{"task_id":"task-1","column_id":"col-2"}`)
	c1.send(t, relay.RelayMessage{
		BoardID:   "board-a",
		MemberID:  "member-1",
		Type:      "task.move",
		Payload:   payload,
		Timestamp: time.Now().Unix(),
		Version:   1,
	})

	// Cliente 2 lo recibe.
	got := c2.receive(t)
	if got.BoardID != "board-a" {
		t.Errorf("BoardID = %q, want %q", got.BoardID, "board-a")
	}
	if got.MemberID != "member-1" {
		t.Errorf("MemberID = %q, want %q", got.MemberID, "member-1")
	}
	if got.Type != "task.move" {
		t.Errorf("Type = %q, want %q", got.Type, "task.move")
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, payload)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want %d", got.Version, 1)
	}

	// El remitente no recibe su propio mensaje.
	c1.setReadDeadline(shortTimeout)
	defer c1.clearReadDeadline()
	if _, err := c1.reader.ReadString('\n'); err == nil {
		t.Errorf("sender received its own message")
	}
}

func TestRelayIsolationBetweenBoards(t *testing.T) {
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllExt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	addr := r.Addr().String()

	// Cliente 1 en el board A.
	c1 := dial(t, addr)
	t.Cleanup(c1.close)
	c1.join(t, "board-a", "member-1")

	// Cliente 3 en el board B (diferente).
	c3 := dial(t, addr)
	t.Cleanup(c3.close)
	c3.join(t, "board-b", "member-3")

	waitForJoin()

	// Cliente 1 envía un mensaje al board A.
	c1.send(t, relay.RelayMessage{
		BoardID:  "board-a",
		MemberID: "member-1",
		Type:     "task.create",
		Payload:  []byte(`{"title":"hola"}`),
	})

	// El cliente del board B no debe recibir nada del board A.
	if got, ok := c3.receiveOrTimeout(); ok {
		t.Errorf("board-b client received message from board-a: %+v", got)
	}
}

func TestRelayDisconnectKeepsOthersConnected(t *testing.T) {
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllExt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	addr := r.Addr().String()

	// Cliente 1 y cliente 2 se unen al board A.
	c1 := dial(t, addr)
	t.Cleanup(c1.close)
	c1.join(t, "board-a", "member-1")
	c2 := dial(t, addr)
	t.Cleanup(c2.close)
	c2.join(t, "board-a", "member-2")

	waitForJoin()

	// Cliente 1 se desconecta inesperadamente.
	c1.close()

	// Un nuevo cliente member-3 se une y envía un mensaje.
	c3 := dial(t, addr)
	t.Cleanup(c3.close)
	c3.join(t, "board-a", "member-3")
	waitForJoin()

	payload := []byte(`{"task_id":"task-9","done":true}`)
	c3.send(t, relay.RelayMessage{
		BoardID:   "board-a",
		MemberID:  "member-3",
		Type:      "task.done",
		Payload:   payload,
		Timestamp: time.Now().Unix(),
		Version:   3,
	})

	// Cliente 2 sigue conectado y recibe el mensaje.
	got := c2.receive(t)
	if got.BoardID != "board-a" {
		t.Errorf("BoardID = %q, want %q", got.BoardID, "board-a")
	}
	if got.MemberID != "member-3" {
		t.Errorf("MemberID = %q, want %q", got.MemberID, "member-3")
	}
	if got.Type != "task.done" {
		t.Errorf("Type = %q, want %q", got.Type, "task.done")
	}
}

// TestRelayPublishReachesBoardMembers verifica que Publish inyecta un
// mensaje en el relay y llega a los demas miembros del board (menos el
// propio remitente), como si lo hubiera enviado un dispositivo unido.
func TestRelayPublishReachesBoardMembers(t *testing.T) {
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllExt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	addr := r.Addr().String()

	c1 := dial(t, addr)
	t.Cleanup(c1.close)
	c1.join(t, "board-a", "member-1")
	c2 := dial(t, addr)
	t.Cleanup(c2.close)
	c2.join(t, "board-a", "member-2")
	waitForJoin()

	payload := []byte(`{"task_id":"task-1","title":"hola"}`)
	if err := r.Publish(relay.RelayMessage{
		BoardID:  "board-a",
		MemberID: "member-1",
		Type:     "task.create",
		Payload:  payload,
		Version:  1,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got := c2.receive(t)
	if got.BoardID != "board-a" {
		t.Errorf("BoardID = %q, want %q", got.BoardID, "board-a")
	}
	if got.MemberID != "member-1" {
		t.Errorf("MemberID = %q, want %q", got.MemberID, "member-1")
	}
	if got.Type != "task.create" {
		t.Errorf("Type = %q, want %q", got.Type, "task.create")
	}
	if string(got.Payload) != string(payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, payload)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want %d", got.Version, 1)
	}

	// member-1 (fromMember) no recibe su propio mensaje publicado.
	c1.setReadDeadline(shortTimeout)
	defer c1.clearReadDeadline()
	if _, err := c1.reader.ReadString('\n'); err == nil {
		t.Errorf("el remitente recibio su propio mensaje publicado")
	}
}

// TestRelayPublishRequiresBoard verifica que Publish rechaza mensajes sin
// board_id. member_id es opcional para mensajes generados por el servidor,
// por lo que Publish no lo exige.
func TestRelayPublishRequiresBoard(t *testing.T) {
	r := relay.NewRelay("127.0.0.1:0")
	r.SetAdmission(admitAllExt)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	if err := r.Publish(relay.RelayMessage{Type: "task.create"}); err == nil {
		t.Error("Publish sin board_id: esperaba error, obtuvo nil")
	}
	// member_id vacio es valido para mensajes del servidor: se difunde a
	// todos los miembros del board.
	if err := r.Publish(relay.RelayMessage{BoardID: "board-a", Type: "task.create"}); err != nil {
		t.Errorf("Publish con member_id vacio: %v, esperaba nil", err)
	}
}

// admitAllExt admite cualquier member.join sin respuesta: estos tests
// prueban el enrutado, no la admisión (admission_test.go la prueba).
func admitAllExt(relay.RelayMessage) (*relay.RelayMessage, error) { return nil, nil }
