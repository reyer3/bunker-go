package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// fakeBoardPeer is a core.BoardPeer that records what the service asked it to
// do, so the port can be tested without a libp2p node.
type fakeBoardPeer struct {
	connected   []string
	published   []core.BoardPeerMessage
	foundPeers  []string
	connectAddr string
}

func (f *fakeBoardPeer) PeerID() string           { return "peer-self" }
func (f *fakeBoardPeer) ListenAddr() string       { return "/ip4/127.0.0.1/tcp/4001" }
func (f *fakeBoardPeer) ConnectedPeers() []string { return f.connected }
func (f *fakeBoardPeer) Connect(ctx context.Context, multiaddr string) error {
	f.connectAddr = multiaddr
	return nil
}
func (f *fakeBoardPeer) Publish(boardID string, msg core.BoardPeerMessage) error {
	f.published = append(f.published, msg)
	return nil
}
func (f *fakeBoardPeer) FindPeers(ctx context.Context, secret string) ([]string, error) {
	return f.foundPeers, nil
}

// TestBoardPeerStatusWithoutPeerIsUnsupported prueba que un daemon sin nodo
// P2P falla con ErrUnsupported en vez de devolver un estado vacío silencioso.
func TestBoardPeerStatusWithoutPeerIsUnsupported(t *testing.T) {
	svc := core.NewService(nil, core.NewRegistry())
	if _, err := svc.BoardPeerStatus(context.Background(), ""); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// TestBoardPeerStatusReportsNodeAndRendezvous prueba que el estado incluye la
// identidad del nodo y, con secreto, los peers del rendezvous.
func TestBoardPeerStatusReportsNodeAndRendezvous(t *testing.T) {
	svc := core.NewService(nil, core.NewRegistry())
	svc.SetBoardPeer(&fakeBoardPeer{connected: []string{"peer-a"}, foundPeers: []string{"peer-b"}})

	status, err := svc.BoardPeerStatus(context.Background(), "secreto")
	if err != nil {
		t.Fatalf("BoardPeerStatus: %v", err)
	}
	if status.PeerID != "peer-self" || status.Addr == "" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Peers) != 1 || status.Peers[0] != "peer-a" {
		t.Fatalf("peers = %v", status.Peers)
	}
	if len(status.RendezvousPeers) != 1 || status.RendezvousPeers[0] != "peer-b" {
		t.Fatalf("rendezvous peers = %v", status.RendezvousPeers)
	}
}

// TestBoardPeerStatusWithoutSecretSkipsRendezvous prueba que sin --secreto no
// se consulta la DHT (el campo queda vacío).
func TestBoardPeerStatusWithoutSecretSkipsRendezvous(t *testing.T) {
	svc := core.NewService(nil, core.NewRegistry())
	svc.SetBoardPeer(&fakeBoardPeer{foundPeers: []string{"peer-b"}})

	status, err := svc.BoardPeerStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("BoardPeerStatus: %v", err)
	}
	if len(status.RendezvousPeers) != 0 {
		t.Fatalf("rendezvous peers = %v, want none", status.RendezvousPeers)
	}
}

// TestBoardPeerSyncPublishesMessage prueba que el delta llega al transporte
// con sus campos intactos y devuelve los peers conectados.
func TestBoardPeerSyncPublishesMessage(t *testing.T) {
	peer := &fakeBoardPeer{connected: []string{"peer-a"}}
	svc := core.NewService(nil, core.NewRegistry())
	svc.SetBoardPeer(peer)

	peers, err := svc.BoardPeerSync(context.Background(), "board-1", "member-1", "task.done", []byte(`{"task_id":"t1"}`), 3)
	if err != nil {
		t.Fatalf("BoardPeerSync: %v", err)
	}
	if len(peers) != 1 || peers[0] != "peer-a" {
		t.Fatalf("peers = %v", peers)
	}
	if len(peer.published) != 1 {
		t.Fatalf("published = %d", len(peer.published))
	}
	got := peer.published[0]
	if got.BoardID != "board-1" || got.MemberID != "member-1" || got.Type != "task.done" || got.Version != 3 {
		t.Fatalf("message = %+v", got)
	}
	if string(got.Payload) != `{"task_id":"t1"}` {
		t.Fatalf("payload = %s", got.Payload)
	}
}
