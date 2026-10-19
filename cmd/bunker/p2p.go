package main

import (
	"context"
	"fmt"
	"io"
	"strings"
)

const p2pUsage = `usage: bunker p2p status [--secreto <texto>] [--json]
       bunker p2p connect <multiaddr> [--json]
       bunker p2p sync <task-id> [--json]`

// cmdP2P dispatches the decentralized board-sync commands. The libp2p node
// lives inside the daemon (started with --p2p-listen); these commands reach it
// through the Backend, exactly like the board commands reach BoardService.
func cmdP2P(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, p2pUsage)
		return 2
	}
	switch args[0] {
	case "status":
		return cmdP2PStatus(ctx, backend, args[1:], stdout, stderr)
	case "connect":
		return cmdP2PConnect(ctx, backend, args[1:], stdout, stderr)
	case "sync":
		return cmdP2PSync(ctx, backend, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, p2pUsage)
		return 2
	}
}

// cmdP2PStatus reports the daemon node's peer id, listen address and
// connected peers. With --secreto it also looks for peers sharing that secret
// in the DHT rendezvous, which answers "¿ve el nodo de la otra ciudad?".
func cmdP2PStatus(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("p2p status", stderr)
	secreto := fs.String("secreto", "", "string secreto compartido para buscar peers en el rendezvous")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 {
		fmt.Fprintln(stderr, p2pUsage)
		return 2
	}

	status, err := backend.BoardPeerStatus(ctx, strings.TrimSpace(*secreto))
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}

	if *jsonOut {
		writeJSON(stdout, status)
		return 0
	}
	fmt.Fprintf(stdout, "ID: %s\n", status.PeerID)
	fmt.Fprintf(stdout, "Addr: %s\n", status.Addr)
	fmt.Fprintf(stdout, "Peers conectados: %d\n", len(status.Peers))
	for _, p := range status.Peers {
		fmt.Fprintf(stdout, "  %s\n", p)
	}
	if status.RendezvousPeers != nil {
		fmt.Fprintf(stdout, "Peers en rendezvous: %d\n", len(status.RendezvousPeers))
		for _, p := range status.RendezvousPeers {
			fmt.Fprintf(stdout, "  %s\n", p)
		}
	}
	return 0
}

// cmdP2PConnect dials a peer from its full multiaddr, the manual discovery
// path when there is no rendezvous.
func cmdP2PConnect(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("p2p connect", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, p2pUsage)
		return 2
	}

	peers, err := backend.BoardPeerConnect(ctx, positionals[0])
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}

	if *jsonOut {
		writeJSON(stdout, map[string]any{"connected": true, "multiaddr": positionals[0], "peers": peers})
		return 0
	}
	fmt.Fprintf(stdout, "conectado a %s\n", positionals[0])
	return 0
}

// cmdP2PSync forces one task delta out through the daemon's open P2P streams.
// It reads the task through the backend and publishes it to every connected
// peer of the task's board.
func cmdP2PSync(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("p2p sync", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, p2pUsage)
		return 2
	}
	taskID := positionals[0]

	item, err := backend.Get(ctx, taskID)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("no se encontró la tarea %q: %w", taskID, err))
	}

	boardID := item.Thread
	peers, err := backend.BoardPeerSync(ctx, boardID, "", "task.sync", nil, 0)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}

	if *jsonOut {
		writeJSON(stdout, map[string]any{"task": taskID, "board": boardID, "synced": true, "peers": peers})
		return 0
	}
	fmt.Fprintf(stdout, "tarea %s sincronizada en el board %s\n", taskID, boardID)
	return 0
}
