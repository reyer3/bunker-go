package main

import (
	"context"
	"encoding/json"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/p2p"
)

// peerTransport adapts the daemon's libp2p node to core.BoardPeer. core never
// imports internal/p2p: this file is the only place where both meet.
type peerTransport struct {
	node *p2p.Node
}

var _ core.BoardPeer = (*peerTransport)(nil)

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
	payload := json.RawMessage(nil)
	if len(msg.Payload) > 0 {
		payload = json.RawMessage(msg.Payload)
	}
	return t.node.Publish(boardID, p2p.RelayMessage{
		BoardID:   msg.BoardID,
		MemberID:  msg.MemberID,
		Type:      msg.Type,
		Payload:   payload,
		Timestamp: msg.Timestamp,
		Version:   msg.Version,
	})
}
