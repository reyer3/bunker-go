// Package p2p implementa un nodo libp2p para sincronizar cambios de tableros
// compartidos entre dos instancias de bunker-go sin servidor central.
//
// Cada nodo es un host libp2p con su propia clave criptográfica (su peer ID).
// Los mensajes viajan sobre el protocolo /bunker/board/1.0.0: un nodo abre un
// stream hacia un peer conocido, envía un mensaje JSON y lo cierra. El nodo
// destino lo lee y lo reparte a quien esté suscrito al board.
//
// go-libp2p es Go puro y compila con CGO_ENABLED=0, sin dependencias dinámicas
// del sistema.
package p2p

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// boardProtocol es el protocolo de aplicación para los mensajes de board.
const boardProtocol protocol.ID = "/bunker/board/1.0.0"

// publishTimeout es el tiempo máximo para abrir un stream y enviar un mensaje.
const publishTimeout = 5 * time.Second

// RelayMessage es el formato que viaja por el nodo P2P. Es idéntico en forma a
// relay.RelayMessage (del relay centralizado) pero este paquete no importa
// internal/relay: las capas de red no comparten dependencias con core.
type RelayMessage struct {
	BoardID   string          `json:"board_id"`
	MemberID  string          `json:"member_id"`
	Type      string          `json:"type"` // "task.create", "task.move", "task.done", "task.comment"
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
	Version   int             `json:"version"`
}

// Node es un nodo libp2p que puede suscribirse a un board y publicar cambios a
// los demás miembros del mismo board.
type Node struct {
	host   host.Host
	mu     sync.Mutex
	subs   map[string]chan<- []byte // boardID -> canal de payloads entrantes
	logger *slog.Logger
}

// NewNode crea un nodo libp2p que escucha en listenAddr (ej. "/ip4/0.0.0.0/tcp/0").
// Genera una clave privada efímera: el peer ID es la identidad de este nodo.
func NewNode(listenAddr string) (*Node, error) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("p2p: generar clave privada: %w", err)
	}

	addr, err := ma.NewMultiaddr(listenAddr)
	if err != nil {
		return nil, fmt.Errorf("p2p: dirección de escucha inválida %q: %w", listenAddr, err)
	}

	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(addr),
	)
	if err != nil {
		return nil, fmt.Errorf("p2p: crear nodo libp2p: %w", err)
	}

	n := &Node{
		host:   h,
		subs:   make(map[string]chan<- []byte),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	h.SetStreamHandler(boardProtocol, n.handleStream)
	return n, nil
}

// Start no hace nada especial: libp2p ya escucha desde que se creó el nodo.
func (n *Node) Start(ctx context.Context) error {
	return nil
}

// Stop cierra el nodo libp2p y todos sus streams.
func (n *Node) Stop() error {
	return n.host.Close()
}

// Addr devuelve la primera dirección multiaddr del nodo, o cadena vacía si no hay.
func (n *Node) Addr() string {
	addrs := n.host.Addrs()
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0].String()
}

// FullAddrs devuelve las direcciones de escucha del nodo con el peer id
// incluido (/ip4/.../p2p/<id>), listas para pasar a Connect de otro nodo.
func (n *Node) FullAddrs() []string {
	addrs := n.host.Addrs()
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String()+"/p2p/"+n.host.ID().String())
	}
	return out
}

// ID devuelve el Peer ID del nodo.
func (n *Node) ID() string {
	return n.host.ID().String()
}

// ConnectedPeers devuelve los IDs de los peers conectados a este nodo.
func (n *Node) ConnectedPeers() []string {
	peers := n.host.Network().Peers()
	ids := make([]string, 0, len(peers))
	for _, p := range peers {
		ids = append(ids, p.String())
	}
	return ids
}

// Connect se conecta a un peer a partir de su multiaddr completa
// (ej. /ip4/203.0.113.5/tcp/4001/p2p/12D3KooW...).
func (n *Node) Connect(ctx context.Context, multiaddr string) error {
	info, err := PeerInfoFromMultiaddr(multiaddr)
	if err != nil {
		return err
	}
	if err := n.host.Connect(ctx, info); err != nil {
		return fmt.Errorf("p2p: conectar con %s: %w", info.ID, err)
	}
	return nil
}

// PeerInfoFromMultiaddr extrae el peer.AddrInfo de una multiaddr con /p2p/<id>.
func PeerInfoFromMultiaddr(multiaddr string) (peer.AddrInfo, error) {
	addr, err := ma.NewMultiaddr(multiaddr)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("p2p: multiaddr inválida %q: %w", multiaddr, err)
	}
	info, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("p2p: multiaddr sin peer id %q: %w", multiaddr, err)
	}
	return *info, nil
}

// Subscribe suscribe un canal a un board. Los payloads entrantes de ese board
// se envían al canal. No bloquea si el canal está lleno: se descarta el
// mensaje para no detener la red.
func (n *Node) Subscribe(boardID string, ch chan<- []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.subs[boardID] = ch
}

// Publish envía msg a todos los peers conectados y lo entrega a los
// suscriptores locales de msg.BoardID.
func (n *Node) Publish(boardID string, msg RelayMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("p2p: codificar mensaje: %w", err)
	}

	// Entrega a suscriptores locales de este board.
	n.mu.Lock()
	ch, ok := n.subs[boardID]
	connected := n.host.Network().Peers()
	n.mu.Unlock()

	if ok {
		select {
		case ch <- msg.Payload:
		default:
			// Canal lleno: descartar para no bloquear la red.
		}
	}

	// Envía a cada peer conectado mediante su propio stream.
	for _, p := range connected {
		go n.sendPeer(p, data)
	}
	return nil
}

// sendPeer abre un stream hacia el peer p y envía data como una línea JSON.
func (n *Node) sendPeer(p peer.ID, data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	s, err := n.host.NewStream(ctx, p, boardProtocol)
	if err != nil {
		n.logger.Warn("p2p: no se pudo abrir stream", "peer", p.String(), "error", err)
		return
	}
	defer s.Close()

	if _, err := s.Write(append(data, '\n')); err != nil {
		n.logger.Warn("p2p: error escribiendo al peer", "peer", p.String(), "error", err)
	}
}

// handleStream lee un mensaje JSON entrante de la red y lo reparte al
// suscriptor local del board correspondiente.
func (n *Node) handleStream(s network.Stream) {
	defer s.Close()

	dec := json.NewDecoder(s)
	var msg RelayMessage
	if err := dec.Decode(&msg); err != nil {
		n.logger.Error("p2p: mensaje mal formado", "error", err)
		return
	}

	n.mu.Lock()
	ch, ok := n.subs[msg.BoardID]
	n.mu.Unlock()
	if !ok {
		return // nadie suscrito a este board en este nodo
	}

	select {
	case ch <- msg.Payload:
	default:
		// Canal lleno: descartar.
	}
}
