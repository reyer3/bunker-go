package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/p2p"
)

// peerTransport adapts the daemon's libp2p node to core.BoardPeer. core never
// imports internal/p2p: this file is the only place where both meet.
type peerTransport struct {
	node *p2p.Node
}

var (
	_ core.BoardPeer           = (*peerTransport)(nil)
	_ core.BoardPeerRequester  = (*peerTransport)(nil)
	_ core.BoardClaimResponder = (*peerTransport)(nil)
)

func newPeerTransport(node *p2p.Node) *peerTransport {
	return &peerTransport{node: node}
}

// PeerID returns the node's peer id on the P2P network.
func (t *peerTransport) PeerID() string { return t.node.ID() }

// ListenAddr returns the node's listen multiaddr.
func (t *peerTransport) ListenAddr() string { return t.node.Addr() }

// ConnectedPeers returns the ids of the peers currently connected.
func (t *peerTransport) ConnectedPeers() []string { return t.node.ConnectedPeers() }

// Connect dials a peer from its full multiaddr.
func (t *peerTransport) Connect(ctx context.Context, multiaddr string) error {
	return t.node.Connect(ctx, multiaddr)
}

// Publish sends one board delta to every connected peer.
func (t *peerTransport) Publish(boardID string, msg core.BoardPeerMessage) error {
	return t.node.Publish(boardID, toP2PMessage(msg))
}

// Request manda un claim a cada peer conectado, de a uno, hasta que alguno
// responda con un welcome: solo el daemon que tiene la invitación puede
// hacerlo, y no hay forma de saber de antemano cuál es. Si nadie da el
// welcome devuelve el último rechazo (el creador dice por qué) o el último
// error de red.
func (t *peerTransport) Request(ctx context.Context, msg core.BoardPeerMessage) (core.BoardPeerMessage, error) {
	peers := t.node.ConnectedPeers()
	if len(peers) == 0 {
		return core.BoardPeerMessage{}, errors.New("sin peers conectados: primero bunker p2p connect <multiaddr> del creador")
	}
	var lastReject *core.BoardPeerMessage
	var lastErr error
	for _, id := range peers {
		reply, err := t.node.Request(ctx, id, toP2PMessage(msg))
		if err != nil {
			lastErr = err
			continue
		}
		out := fromP2PMessage(reply)
		if out.Type == core.WelcomeMessageType {
			return out, nil
		}
		lastReject = &out
	}
	if lastReject != nil {
		return *lastReject, nil
	}
	return core.BoardPeerMessage{}, fmt.Errorf("ningún peer respondió el claim: %w", lastErr)
}

// OnClaim entrega los claims entrantes por P2P a fn y devuelve su respuesta
// por el mismo stream.
func (t *peerTransport) OnClaim(fn func(core.BoardPeerMessage) core.BoardPeerMessage) {
	t.node.OnRequest(func(msg p2p.RelayMessage) p2p.RelayMessage {
		return toP2PMessage(fn(fromP2PMessage(msg)))
	})
}

func toP2PMessage(m core.BoardPeerMessage) p2p.RelayMessage {
	var payload json.RawMessage
	if len(m.Payload) > 0 {
		payload = json.RawMessage(m.Payload)
	}
	return p2p.RelayMessage{BoardID: m.BoardID, MemberID: m.MemberID, Type: m.Type, Payload: payload, Timestamp: m.Timestamp, Version: m.Version}
}

func fromP2PMessage(m p2p.RelayMessage) core.BoardPeerMessage {
	return core.BoardPeerMessage{BoardID: m.BoardID, MemberID: m.MemberID, Type: m.Type, Payload: []byte(m.Payload), Timestamp: m.Timestamp, Version: m.Version}
}
