package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/relay"
)

const boardTestTimeout = 5 * time.Second

// bboardTestClient es un cliente de prueba que se conecta al relay por TCP.
type bboardTestClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func bboardDial(t *testing.T, addr string) *bboardTestClient {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return &bboardTestClient{conn: conn, reader: bufio.NewReader(conn)}
}

func (c *bboardTestClient) close() { _ = c.conn.Close() }

func (c *bboardTestClient) send(t *testing.T, msg relay.RelayMessage) {
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

func (c *bboardTestClient) join(t *testing.T, boardID, memberID string) {
	t.Helper()
	c.send(t, relay.RelayMessage{BoardID: boardID, MemberID: memberID, Type: "member.join"})
}

// startTestRelay arranca un relay real en puerto aleatorio y devuelve su
// direccion junto con un cleanup que lo detiene.
func startTestRelay(t *testing.T) (string, func()) {
	t.Helper()
	r := relay.NewRelay("127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), boardTestTimeout)
	if err := r.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	return r.Addr().String(), func() {
		cancel()
		_ = r.Stop()
	}
}

func waitForJoin() {
	time.Sleep(150 * time.Millisecond)
}

// TestBoardJoinRelayRegistersAndReceivesSync comprueba que relayJoinMember
// conecta al relay, se registra con member.join y mantiene la conexion abierta
// para recibir los deltas del board (un miembro del mismo board recibe lo que
// envia otro).
func TestBoardJoinRelayRegistersAndReceivesSync(t *testing.T) {
	addr, stop := startTestRelay(t)
	defer stop()

	// m1 se une al board-1 a traves del relay (simula lo que hace
	// cmdBoardJoin tras BoardJoin).
	joinCtx, joinCancel := context.WithCancel(context.Background())
	t.Cleanup(joinCancel)
	conn1, err := relayJoinMember(joinCtx, addr, "board-1", "m1")
	if err != nil {
		t.Fatalf("relayJoinMember: %v", err)
	}
	defer conn1.Close()

	// m2 se une al mismo board y envia un delta de tarea.
	c2 := bboardDial(t, addr)
	defer c2.close()
	c2.join(t, "board-1", "m2")
	waitForJoin()

	c2.send(t, relay.RelayMessage{
		BoardID:   "board-1",
		MemberID:  "m2",
		Type:      "task.create",
		Payload:   []byte(`{"title":"hola"}`),
		Timestamp: time.Now().Unix(),
	})

	// m1 recibe el delta (esta registrado en el board y la conexion esta abierta).
	conn1.SetReadDeadline(time.Now().Add(boardTestTimeout))
	defer conn1.SetReadDeadline(time.Time{})
	line, err := bufio.NewReader(conn1).ReadString('\n')
	if err != nil {
		t.Fatalf("m1 no recibio sincronizacion del relay: %v", err)
	}
	var got relay.RelayMessage
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.BoardID != "board-1" {
		t.Errorf("BoardID = %q, want %q", got.BoardID, "board-1")
	}
	if got.MemberID != "m2" {
		t.Errorf("MemberID = %q, want %q", got.MemberID, "m2")
	}
	if got.Type != "task.create" {
		t.Errorf("Type = %q, want %q", got.Type, "task.create")
	}
	if string(got.Payload) != `{"title":"hola"}` {
		t.Errorf("Payload = %s, want %s", got.Payload, `{"title":"hola"}`)
	}
}

// TestCmdBoardJoinWithRelayUpSucceeds comprueba que cmdBoardJoin llama a
// BoardJoin y, con el relay disponible, no falla por el registro en el relay.
func TestCmdBoardJoinWithRelayUpSucceeds(t *testing.T) {
	addr, stop := startTestRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	// relayJoinMember mantiene abierta la conexion de relay hasta ctx.Done();
	// Relay.Stop espera a que los handlers de cliente terminen. Como el fake
	// backend devuelve BoardID vacio, la conexion no queda registrada en el
	// relay y Stop() no la cierra: cancelamos ctx primero para cerrarla y
	// evitar un interbloqueo en el apagado.
	defer func() { cancel(); stop() }()

	t.Setenv("BUNKER_RELAY_ADDR", addr)

	backend := newFakeBackend()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	code := cmdBoardJoin(ctx, backend, []string{"bunker-invite-code", "--member-id", "m1"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("cmdBoardJoin = %d, want 0", code)
	}
	if len(backend.boardJoinCalls) != 1 {
		t.Fatalf("boardJoinCalls = %d, want 1", len(backend.boardJoinCalls))
	}
	if got := backend.boardJoinCalls[0]; got.Code != "bunker-invite-code" || got.MemberID != "m1" {
		t.Errorf("boardJoinCalls[0] = %+v, want Code=bunker-invite-code MemberID=m1", got)
	}
}

// TestCmdBoardJoinRelayDownIsWarningOnly comprueba que un relay no disponible
// no hace fallar el join del board: el join ya esta confirmado por el backend.
func TestCmdBoardJoinRelayDownIsWarningOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Setenv("BUNKER_RELAY_ADDR", "127.0.0.1:1") // nada escuchando

	backend := newFakeBackend()
	stdout := &strings.Builder{}
	stderr := &strings.Builder{}
	code := cmdBoardJoin(ctx, backend, []string{"code", "--member-id", "m1"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("cmdBoardJoin = %d, want 0 (join debe seguir OK aunque el relay este caido)", code)
	}
	if len(backend.boardJoinCalls) != 1 {
		t.Fatalf("boardJoinCalls = %d, want 1", len(backend.boardJoinCalls))
	}
}

// TestBoardUsageDocumentsRelayFlag comprueba que la ayuda documenta --relay.
func TestBoardUsageDocumentsRelayFlag(t *testing.T) {
	if !strings.Contains(boardUsage, "--relay") {
		t.Error("boardUsage no documenta el flag --relay")
	}
}
