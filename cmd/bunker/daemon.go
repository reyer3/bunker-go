package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/p2p"
	"github.com/reyer3/bunker-go/internal/relay"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
	"github.com/reyer3/bunker-go/internal/update"
)

func cmdDaemonMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fakeMode := fs.Bool("fake", false, "seed demo data via the in-memory fake channel instead of configured accounts")
	relayAddr := fs.String("relay-addr", "127.0.0.1:0", "direccion en la que escucha el relay ligero de tableros compartidos (puerto 0 = asignado por el sistema)")
	p2pListen := fs.String("p2p-listen", "", "direccion multiaddr del nodo libp2p para sincronizacion P2P de tableros (ej. /ip4/0.0.0.0/tcp/4001); vacio lo deshabilita")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runDaemon(ctx, config.StateDir(), config.AvatarCacheDir(), rpc.DefaultSocketPath(), *fakeMode, *relayAddr, *p2pListen, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// runDaemon opens the store, wires the registry (demo or configured),
// starts every adapter's Run loop feeding the store, and serves the RPC
// socket until ctx is canceled. It returns nil on a clean, ctx-triggered
// shutdown. avatarCacheDir is passed explicitly (like stateDir/
// socketPath) rather than read from config internally, so tests always
// point it at their own temp dir and never a real ~/.cache.
func runDaemon(ctx context.Context, stateDir, avatarCacheDir, socketPath string, fakeMode bool, relayAddr, p2pListen string, stdout, stderr io.Writer) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("daemon: create state dir %s: %w", stateDir, err)
	}

	st, err := store.Open(filepath.Join(stateDir, "bunker.db"))
	if err != nil {
		return fmt.Errorf("daemon: open store: %w", err)
	}
	defer st.Close()

	// Shared boards (kanban) service backed by the same SQLite store.
	// core.Store implements core.BoardStore, so boards, columns, tasks,
	// comments, members and invite codes all live in bunker.db.
	boardSvc = core.NewBoardService(st)

	// Ligero relay de sincronizacion de tableros compartidos. Escucha en
	// relayAddr (127.0.0.1:0 = puerto asignado por el sistema) y acepta
	// conexiones de los dispositivos que se unen a un board.
	boardRelay = relay.NewRelay(relayAddr)
	if err := boardRelay.Start(ctx); err != nil {
		return fmt.Errorf("daemon: start relay %s: %w", relayAddr, err)
	}
	defer boardRelay.Stop()
	if addr := boardRelay.Addr(); addr != nil {
		fmt.Fprintf(stdout, "bunker: relay listening on %s\n", addr.String())
	}

	reg, cfg, err := loadRegistry(fakeMode, stdout)
	if err != nil {
		return err
	}

	if fakeMode {
		if err := seedDemoTodos(ctx, st, time.Now()); err != nil {
			return err
		}
	}

	svc := core.NewService(st, reg)
	// The relay refuses every client without an admission check, so it
	// must learn to verify claims and member proofs against this store.
	boardRelay.SetAdmission(boardRelayAdmission(svc))
	// Nodo libp2p para sincronizar tableros sin servidor central. Es
	// opcional (--p2p-listen): si no se pide o falla, el daemon sigue con
	// el relay. Un fallo de P2P nunca debe impedir guardar cambios.
	var peer *p2p.Node
	if strings.TrimSpace(p2pListen) != "" {
		node, err := p2p.NewNode(p2pListen)
		if err != nil {
			fmt.Fprintf(stderr, "bunker: p2p deshabilitado: %v\n", err)
		} else {
			peer = node
			defer func() {
				if err := node.Stop(); err != nil {
					fmt.Fprintf(stderr, "bunker: cerrar nodo p2p: %v\n", err)
				}
			}()
			svc.SetBoardPeer(newPeerTransport(node))
			fmt.Fprintf(stdout, "bunker: p2p listening on %s (peer %s)\n", node.Addr(), node.ID())
		}
	}

	svc.SetAvatarCacheDir(avatarCacheDir)
	// Attachments a client on another machine downloads wait here
	// between download_open and download_close (core/staging.go).
	svc.SetStagingDir(filepath.Join(stateDir, "staging"))
	health := core.NewHealthTracker()
	svc.SetHealthTracker(health)
	checker := newUpdateChecker(currentBuild(), cfg, fakeMode, stateDir, stderr)
	svc.SetUpdateSource(checker)

	// Notificar al relay cada cambio de tarea para que los demas miembros
	// del board lo reciban en tiempo real. Se conecta tanto al Service que
	// atiende el RPC (svc.boardSvc) como al boardSvc package-level usado
	// por la CLI en proceso.
	notifyBoard := func(ev core.BoardEvent) error {
		// Cifrar en el limite de transporte: lo que viaja por el relay y por
		// los peers P2P es el contenido de la tarea (titulo, descripcion,
		// tags), y la promesa del feature es que el relay y los peers nunca
		// lo leen. Un fallo de cifrado (clave corrupta) no publica el delta:
		// el store ya tiene el cambio, y algo de confidencialidad vale mas
		// que un sync cuyo contenido se fuga.
		sent, encErr := encryptBoardDelta(st, ev)
		if errors.Is(encErr, errNoBoardKey) {
			// Tablero sin clave: nunca prometio E2EE, sale como estaba.
			sent = ev.Payload
		} else if encErr != nil {
			fmt.Fprintf(stderr, "bunker: board %s: no publicar %s: %v\n", ev.BoardID, ev.Type, encErr)
			return nil
		}
		ev.Payload = sent
		msg := relay.RelayMessage{
			BoardID:   ev.BoardID,
			MemberID:  ev.MemberID,
			Type:      ev.Type,
			Payload:   ev.Payload,
			Timestamp: ev.Timestamp,
			Version:   ev.Version,
		}
		err := boardRelay.Publish(msg)

		// Mismo evento por el nodo P2P, si está iniciado. El error de P2P
		// no se propaga: el cambio ya está en el store y el relay central
		// sigue siendo el camino principal.
		if peer != nil {
			p2pMsg := p2p.RelayMessage{
				BoardID:   ev.BoardID,
				MemberID:  ev.MemberID,
				Type:      ev.Type,
				Payload:   ev.Payload,
				Timestamp: ev.Timestamp,
				Version:   ev.Version,
			}
			if perr := peer.Publish(ev.BoardID, p2pMsg); perr != nil {
				fmt.Fprintf(stderr, "bunker: p2p publicar %s: %v\n", ev.Type, perr)
			}
		}
		return err
	}
	svc.SetBoardNotifier(notifyBoard)
	boardSvc.SetBoardNotifier(notifyBoard)

	// Consumir los deltas que llegan del otro lado (relay y P2P) y
	// aplicarlos en el store local: la mitad receptora de la
	// sincronizacion. Se conecta tal cual el consumer de boardSync, y
	// member.join no aplica nada (el relay solo registra al cliente).
	syncBoard := boardSyncConsumer(st, svc, stderr)
	boardRelay.OnMessage(func(msg relay.RelayMessage) {
		syncBoard(msg.BoardID, msg.MemberID, msg.Type, msg.Payload, msg.Timestamp, msg.Version)
	})
	if peer != nil {
		peer.OnInbound(func(msg p2p.RelayMessage) {
			syncBoard(msg.BoardID, msg.MemberID, msg.Type, msg.Payload, msg.Timestamp, msg.Version)
		})
	}

	srv := rpc.NewServer(svc)

	wg := startAdapters(ctx, reg, st, stdout, stderr, health)
	wg.Add(1)
	go func() {
		defer wg.Done()
		checker.Run(ctx)
	}()

	fmt.Fprintf(stdout, "bunker: daemon listening on %s\n", socketPath)
	serveErr := srv.Serve(ctx, socketPath)
	wg.Wait()

	// K3's availability lease reverts on daemon shutdown, same as an
	// explicit blur: a WhatsApp account left "available" must not stay
	// that way after the daemon (and therefore every chat view) is gone.
	svc.ShutdownPresence()

	if ctx.Err() != nil {
		return nil
	}
	return serveErr
}

// startAdapters launches every registered adapter under an
// adapterSupervisor, in its own goroutine, feeding st, and returns the
// *sync.WaitGroup the caller waits on for a clean shutdown. Before an
// adapter's Run starts, if it implements core.Retrier (currently only the
// matrix adapter), it gets one chance to re-fetch and decrypt items it
// previously stored undecryptable -- covering both "the daemon runs it at
// startup" and "after an import" (a `bunker link matrix --recovery-key`
// or `bunker import-keys` run followed by a daemon restart reaches this
// same path). A retry failure is logged, never fatal: Run still starts,
// and the next daemon restart tries again.
//
// R1: unlike a one-shot goroutine, the supervisor restarts Run for as
// long as ctx is live whenever it returns (capped exponential backoff
// with jitter, reset after a run lasting at least adapterHealthyRun), so
// a Matrix sync error or a mail reconnect loop giving up no longer
// silently drops that channel for the rest of the process's life.
// Adapter lifecycle is logged via slog to stderr with channel/account
// attributes, so journald carries every restart even though the process
// itself keeps running.
func startAdapters(ctx context.Context, reg *core.Registry, st core.Store, stdout, stderr io.Writer, health *core.HealthTracker) *sync.WaitGroup {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	return startAdaptersWithSupervisor(ctx, reg, st, newAdapterSupervisor(logger, health))
}

// startAdaptersWithSupervisor is startAdapters' testable core: it takes
// an already-built adapterSupervisor, so tests can inject a fake clock
// and backoff to prove the restart policy deterministically.
func startAdaptersWithSupervisor(ctx context.Context, reg *core.Registry, st core.Store, sup *adapterSupervisor) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, adapter := range reg.List() {
		wg.Add(1)
		go func(a core.Adapter) {
			defer wg.Done()
			sup.supervise(ctx, a, st)
		}(adapter)
	}
	return &wg
}

// loadRegistry builds the demo registry in --fake mode, or the configured
// one otherwise. A missing config file is not an error: the daemon starts
// with no accounts so "bunker daemon" alone still serves list/counts
// against an empty store while Alice sets up ~/.config/bunker-go. The
// config it read is returned too (nil in --fake mode or without a file)
// for the daemon's other settings.
func loadRegistry(fakeMode bool, stdout io.Writer) (*core.Registry, *config.Config, error) {
	if fakeMode {
		return demoRegistry(), nil, nil
	}

	cfg, err := config.LoadDefault()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(stdout, "bunker: no config file at %s, starting with no accounts\n", config.ConfigPath())
			return core.NewRegistry(), nil, nil
		}
		return nil, nil, fmt.Errorf("daemon: load config: %w", err)
	}
	reg, err := buildRegistry(cfg)
	return reg, cfg, err
}

// updateCheckEnabled decides whether the daemon looks for new releases:
// only a release build, never the --fake demo (which must stay offline),
// and not when config says "[update] check = false". No config file
// means the default, on.
func updateCheckEnabled(build buildInfo, cfg *config.Config, fakeMode bool) bool {
	if !build.Release || fakeMode {
		return false
	}
	return cfg == nil || cfg.Update.Check
}

// updateAPITimeout bounds one request to the GitHub releases API, from
// the daemon's check or from "bunker update".
const updateAPITimeout = 30 * time.Second

// newUpdateChecker is the daemon's release check, disabled per
// updateCheckEnabled. Its log goes to stderr at debug level for
// failures, so an offline machine is never noisy.
func newUpdateChecker(build buildInfo, cfg *config.Config, fakeMode bool, stateDir string, stderr io.Writer) *update.Checker {
	return &update.Checker{
		Current:   build.Version,
		Enabled:   updateCheckEnabled(build, cfg, fakeMode),
		Client:    &http.Client{Timeout: updateAPITimeout},
		CachePath: filepath.Join(stateDir, update.CacheFile),
		Logger:    slog.New(slog.NewTextHandler(stderr, nil)),
	}
}
