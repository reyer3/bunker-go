// Command bunker is the single binary for bunker-go: it runs the daemon
// that talks to mail/WhatsApp/Matrix adapters and stores their items, and
// it is the CLI client Alice and Claude Code drive that daemon from.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/tui"
	"golang.org/x/term"
)

// shortCommandTimeout bounds every CLI↔daemon call except send/reply
// (see commandTimeout): generous enough for a slow IMAP/Matrix round
// trip, but the CLI must still never hang forever against a wedged
// daemon.
const shortCommandTimeout = 30 * time.Second

// sendReplyTimeout is send/reply's deadline (T13e): human emulation and a
// broadcast's inter-recipient pauses can legitimately take minutes (up to
// ~10 recipients * (15s composing + 8s pause) ~= 230s), so these two
// commands get a much longer budget than every other command.
const sendReplyTimeout = 15 * time.Minute

// commandTimeout returns the context deadline run() applies for cmd
// (args[0]): the long deadline for send/reply/download, the short one for
// everything else. render sets its own 200ms budget independently (see
// cmdRender) and never goes through this path. download shares
// send/reply's longer budget because it can move up to
// core.DefaultMaxDownloadBytes (100 MB) over a slow IMAP/WhatsApp
// connection — shortCommandTimeout's 30s is comfortably enough for every
// other command but not guaranteed for that.
func commandTimeout(cmd string) time.Duration {
	if cmd == "send" || cmd == "reply" || cmd == "download" {
		return sendReplyTimeout
	}
	return shortCommandTimeout
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin *os.File, stdout, stderr *os.File) int {
	return runWithDependencies(args, stdin, stdout, stderr, runDependencies{
		isTerminal: func(file *os.File) bool { return term.IsTerminal(int(file.Fd())) },
		dial:       func(ctx context.Context, path string) (tui.Client, error) { return rpc.DialContext(ctx, path) },
		startTUI:   startTUI,
		getenv:     os.Getenv,
		herdrRun:   execHerdrRunner(os.Getenv, exec.LookPath),
	})
}

type runDependencies struct {
	isTerminal func(*os.File) bool
	dial       func(context.Context, string) (tui.Client, error)
	startTUI   func(tui.Client, io.Reader, io.Writer, tuiLaunch) error
	// getenv and herdrRun serve "bunker open" (BUNKER_OPEN_ID) and
	// "bunker sidebar" (HERDR_ENV, and herdr to open a conversation in).
	// A nil getenv reads as an empty environment, so tests stay hermetic.
	getenv   func(string) string
	herdrRun herdrRunner
}

func (d runDependencies) env(key string) string {
	if d.getenv == nil {
		return ""
	}
	return d.getenv(key)
}

// tuiLaunch is how the TUI starts: the full inbox (zero value), the
// compact sidebar, or one conversation. It is a plain struct rather than
// tui options so dispatch tests can see what was asked for.
type tuiLaunch struct {
	sidebar bool
	openID  string
	// opener opens a conversation outside this TUI (a herdr pane); nil
	// opens it in place.
	opener func(id string) error
}

func (l tuiLaunch) options() []tui.Option {
	var opts []tui.Option
	if l.sidebar {
		opts = append(opts, tui.WithSidebar())
	}
	if l.opener != nil {
		opts = append(opts, tui.WithExternalOpener(l.opener))
	}
	if l.openID != "" {
		opts = append(opts, tui.WithOpenItem(l.openID))
	}
	return opts
}

func startTUI(client tui.Client, input io.Reader, output io.Writer, launch tuiLaunch) error {
	return tui.Run(client, input, output, launch.options()...)
}

func runWithDependencies(args []string, stdin *os.File, stdout, stderr io.Writer, deps runDependencies) int {
	if len(args) == 0 {
		if !deps.isTerminal(stdin) {
			fmt.Fprint(stderr, topLevelUsage)
			return 2
		}
		return runTUI(stdin, stdout, stderr, deps, tuiLaunch{})
	}
	switch args[0] {
	case "open":
		return cmdOpen(args[1:], stdin, stdout, stderr, deps)
	case "sidebar":
		return cmdSidebar(args[1:], stdin, stdout, stderr, deps)
	}
	return runCommandLine(args, stdin, stdout, stderr)
}

// runTUI dials the daemon and runs the TUI on the terminal; every way of
// starting it ("bunker", "bunker sidebar", "bunker open") shares it, so
// they fail the same way when the daemon is down.
func runTUI(stdin *os.File, stdout, stderr io.Writer, deps runDependencies, launch tuiLaunch) int {
	socket := rpc.DefaultSocketPath()
	ctx, cancel := context.WithTimeout(context.Background(), shortCommandTimeout)
	client, err := deps.dial(ctx, socket)
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot reach bunker daemon at %s: %v\n", socket, err)
		fmt.Fprintln(stderr, "hint: start it with 'bunker daemon' (or 'bunker daemon --fake' to try it without real accounts)")
		return 1
	}
	queries := tui.NewQueryClient(client, func(ctx context.Context) (tui.Client, error) { return deps.dial(ctx, socket) })
	defer queries.Close()
	if err := deps.startTUI(queries, stdin, stdout, launch); err != nil {
		fmt.Fprintln(stderr, "error: terminal UI:", err)
		return 1
	}
	return 0
}

func runCommandLine(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, topLevelUsage)
		return 0
	case "daemon":
		return cmdDaemonMain(args[1:], stdout, stderr)
	case "render":
		return cmdRender(context.Background(), args[1:], stdout, stderr)
	case "app":
		return cmdApp(args[1:], stdout, stderr, defaultAppDeps)
	case "mcp":
		return cmdMCP(args[1:], stderr)
	case "herdr":
		return cmdHerdr(context.Background(), args[1:], stdout, stderr, defaultHerdrDeps())
	case "link":
		cfg, err := config.LoadDefault()
		if err != nil {
			fmt.Fprintln(stderr, "error: load config:", err)
			return 1
		}
		return cmdLink(context.Background(), cfg, args[1:], stdin, stdout, stderr)
	case "import-keys":
		cfg, err := config.LoadDefault()
		if err != nil {
			fmt.Fprintln(stderr, "error: load config:", err)
			return 1
		}
		return cmdImportKeys(context.Background(), cfg, args[1:], stdin, stdout, stderr)
	}

	socket := rpc.DefaultSocketPath()
	client, err := rpc.Dial(socket)
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot reach bunker daemon at %s: %v\n", socket, err)
		fmt.Fprintln(stderr, "hint: start it with 'bunker daemon' (or 'bunker daemon --fake' to try it without real accounts)")
		return 1
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout(args[0]))
	defer cancel()
	return runWithBackend(ctx, client, args, stdin, stdout, stderr)
}
