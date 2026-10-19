package main

import (
	"net"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/relay"
)

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
