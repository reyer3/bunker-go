// Command bunker is the single binary for bunker-go: it runs the daemon
// that talks to mail/WhatsApp/Matrix adapters and stores their items, and
// it is the CLI client Alice and Claude Code drive that daemon from.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/rpc"
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
// (args[0]): the long deadline for send/reply, the short one for
// everything else. render sets its own 200ms budget independently (see
// cmdRender) and never goes through this path.
func commandTimeout(cmd string) time.Duration {
	if cmd == "send" || cmd == "reply" {
		return sendReplyTimeout
	}
	return shortCommandTimeout
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin *os.File, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, topLevelUsage)
		return 2
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, topLevelUsage)
		return 0
	case "daemon":
		return cmdDaemonMain(args[1:], stdout, stderr)
	case "render":
		return cmdRender(context.Background(), args[1:], stdout, stderr)
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
