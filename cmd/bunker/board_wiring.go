package main

import (
	"errors"
	"fmt"
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
	return crypto.Encrypt(ev.Payload)
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
