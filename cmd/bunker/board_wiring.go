package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/reyer3/bunker-go/internal/board"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
)

// errNoBoardKey significa que el board no tiene clave de cifrado almacenada:
// el usuario nunca activo el cifrado E2EE, y el delta viaja como estaba.
var errNoBoardKey = errors.New("board sin clave de cifrado")

// encryptBoardDelta cifra el payload del evento con la clave simétrica del
// board antes de que cruce el relay o la red P2P (el relay y los peers son
// intermediarios y la promesa del feature es que nunca leen el contenido).
// Un board sin clave devuelve errNoBoardKey: nunca prometió E2EE y el delta
// sale sin transformación; cualquier otro error (store roto, clave corrupta)
// sube para que quien publica decida no enviar el delta en claro.
func encryptBoardDelta(st core.BoardStore, ev core.BoardEvent) ([]byte, error) {
	if len(ev.Payload) == 0 {
		return ev.Payload, errNoBoardKey
	}
	b, err := st.GetBoard(ev.BoardID)
	if err != nil {
		return nil, fmt.Errorf("board %s: %w", ev.BoardID, err)
	}
	if len(b.EncryptionKey) == 0 {
		return ev.Payload, errNoBoardKey
	}
	crypto, err := board.NewBoardCrypto()
	if err != nil {
		return nil, fmt.Errorf("board %s: cifrador: %w", ev.BoardID, err)
	}
	crypto.SetPeerKey(b.EncryptionKey)
	sealed, err := crypto.Encrypt(ev.Payload)
	if err != nil {
		return nil, fmt.Errorf("board %s: cifrar: %w", ev.BoardID, err)
	}
	// The transports carry the payload as a json.RawMessage, so raw
	// ciphertext would break the JSON line. A []byte marshals to a base64
	// JSON string.
	return json.Marshal(sealed)
}

// syncableDeltaType dice si un tipo de mensaje del transporte transporta un
// delta aplicable. member.join no es un delta (es el registro del cliente
// en el relay) y los tipos desconocidos se descartan en silencio.
func syncableDeltaType(typ string) bool {
	switch typ {
	case "task.create", "task.move", "task.done", "task.comment":
		return true
	}
	return false
}

// boardSyncConsumer devuelve la función que el daemon conecta a
// Relay.OnMessage y Node.OnInbound: descifra el delta entrante con la clave
// simétrica del board (la misive que el dispositivo emisor usó) y lo aplica
// en el store local vía Service.ApplyBoardEvent. member.join y los otros
// tipos no aplicables no tocan el store; errores de clave o store van al
// stderr sin bloquear la sincronización del resto.
func boardSyncConsumer(st core.BoardStore, svc *core.Service, stderr io.Writer) func(boardID, memberID, typ string, payload []byte, timestamp int64, version int) {
	return func(boardID, memberID, typ string, payload []byte, timestamp int64, version int) {
		if !syncableDeltaType(typ) {
			return
		}
		plain, derr := decryptBoardDelta(st, boardID, payload)
		if derr != nil && !errors.Is(derr, errNoBoardKey) {
			fmt.Fprintf(stderr, "bunker: board %s: recibir %s: %v\n", boardID, typ, derr)
			return
		}
		ev := core.BoardEvent{
			BoardID:   boardID,
			MemberID:  memberID,
			Type:      typ,
			Payload:   plain,
			Timestamp: timestamp,
			Version:   version,
		}
		if aerr := svc.ApplyBoardEvent(ev); aerr != nil {
			fmt.Fprintf(stderr, "bunker: board %s: aplicar %s: %v\n", boardID, typ, aerr)
		}
	}
}

// decryptBoardDelta descifra un payload de transporte con la clave del
// board. Un board sin clave nunca prometió E2EE: el payload vuelve tal cual
// (espejo exacto de encryptBoardDelta). El payload cifrado llega como el
// string JSON base64 que produjo encryptBoardDelta.
func decryptBoardDelta(st core.BoardStore, boardID string, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return payload, errNoBoardKey
	}
	b, err := st.GetBoard(boardID)
	if err != nil {
		return nil, fmt.Errorf("board %s: %w", boardID, err)
	}
	if len(b.EncryptionKey) == 0 {
		return payload, errNoBoardKey
	}
	crypto, err := board.NewBoardCrypto()
	if err != nil {
		return nil, fmt.Errorf("board %s: cifrador: %w", boardID, err)
	}
	var sealed []byte
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return nil, fmt.Errorf("board %s: payload cifrado mal formado: %w", boardID, err)
	}
	crypto.SetPeerKey(b.EncryptionKey)
	return crypto.Decrypt(sealed)
}

// boardSvc is the shared-boards (kanban) service. runDaemon assigns it
// after opening the store; core.Store implements core.BoardStore, so the
// SQLite store opened in runDaemon backs it. It is nil before runDaemon
// starts, and RPC handlers or CLI commands that need board operations read
// it here (or via BoardService()).
var boardSvc *core.BoardService

// BoardService returns the daemon's shared-boards service, or nil if the
// daemon has not started it yet.
func BoardService() *core.BoardService {
	return boardSvc
}

// boardRelay is the in-process relay for real-time shared-board sync. It
// listens on 127.0.0.1:0 (port assigned by the system) and re-sends task
// changes to the other members of each board. runDaemon assigns and starts
// it; it is nil before the daemon starts it.
var boardRelay *relay.Relay

// BoardRelayAddr returns the address the relay is listening on, or nil if
// the daemon has not started it yet. Los clientes se conectan por TCP a
// esta dirección para recibir los cambios de los demás miembros.
func BoardRelayAddr() net.Addr {
	if boardRelay != nil {
		return boardRelay.Addr()
	}
	return nil
}

// boardRelayAdmission es la admisión que el daemon conecta al relay
// (Relay.SetAdmission): un member.claim se valida contra las invitaciones
// del store y se responde con el welcome; un member.join solo registra a
// un miembro que prueba conocer la clave del board. Cualquier otra cosa se
// rechaza: el relay deja de registrar clientes sin prueba.
func boardRelayAdmission(svc *core.Service) relay.AdmitFunc {
	return func(msg relay.RelayMessage) (*relay.RelayMessage, error) {
		reply, err := svc.BoardAnswer(fromRelayMessage(msg))
		if err != nil {
			return nil, err
		}
		if msg.Type != core.ClaimMessageType {
			return nil, nil // un join aceptado no responde nada
		}
		out := toRelayMessage(reply)
		return &out, nil
	}
}

func fromRelayMessage(m relay.RelayMessage) core.BoardPeerMessage {
	return core.BoardPeerMessage{BoardID: m.BoardID, MemberID: m.MemberID, Type: m.Type, Payload: []byte(m.Payload), Timestamp: m.Timestamp, Version: m.Version}
}

func toRelayMessage(m core.BoardPeerMessage) relay.RelayMessage {
	var payload json.RawMessage
	if len(m.Payload) > 0 {
		payload = json.RawMessage(m.Payload)
	}
	return relay.RelayMessage{BoardID: m.BoardID, MemberID: m.MemberID, Type: m.Type, Payload: payload, Timestamp: m.Timestamp, Version: m.Version}
}

// claimReplyAcceptor adopta la respuesta a un claim en el store del
// dispositivo que se une: el Backend del CLI (RPC al daemon local) o un
// core.Service en proceso.
type claimReplyAcceptor interface {
	BoardAcceptClaimReply(ctx context.Context, code, boardID string, reply core.BoardPeerMessage) (*core.Member, error)
}

// claimViaRelay hace el claim contra el relay del creador en addr y adopta
// el welcome con acc. El código nunca viaja: el claim lleva solo la prueba.
func claimViaRelay(ctx context.Context, acc claimReplyAcceptor, addr, code, boardID, memberID, name, publicKey string) (*core.Member, error) {
	msg, err := core.NewClaimMessage(code, boardID, memberID, name, publicKey)
	if err != nil {
		return nil, err
	}
	reply, err := relay.Claim(ctx, addr, toRelayMessage(msg))
	if err != nil {
		return nil, err
	}
	return acc.BoardAcceptClaimReply(ctx, code, boardID, fromRelayMessage(reply))
}
