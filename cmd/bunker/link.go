package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/reyer3/bunker-go/internal/channel/matrix"
	"github.com/reyer3/bunker-go/internal/channel/whatsapp"
	"github.com/reyer3/bunker-go/internal/config"
)

// linkUsage documents `bunker link`, the one command family that drives an
// adapter's own link/login flow directly against the real account instead
// of the daemon: there is no session yet for the daemon to serve, so this
// runs client-side, straight from the CLI process.
const linkUsage = `bunker link <channel> <account> [flags]

Channels:
  whatsapp <account>                                 QR-pair a WhatsApp account
  matrix <account> [--recovery-key] [--recovery-key-stdin]
                                                      SSO login for a Matrix account,
                                                      then optionally import its
                                                      recovery key
`

// whatsappLinkFunc, matrixLoginFunc and matrixImportRecoveryKeyFunc are
// the adapters' own exported link/login/import entry points, held as
// package vars so tests can substitute a fake: this command, and every
// test exercising it, must never dial a real WhatsApp or Matrix account.
var (
	whatsappLinkFunc            = whatsapp.Link
	matrixLoginFunc             = matrix.LoginAccount
	matrixImportRecoveryKeyFunc = matrix.ImportRecoveryKeyForAccount
)

// normalizeSecret strips every whitespace character (spaces, tabs,
// newlines alike), rejoining what remains with no separator. A Matrix
// recovery key has no meaningful internal whitespace -- the spec's own
// human-readable form only adds spaces for visual grouping -- so this is
// safe, and it also repairs a key an extractor wrapped across lines (the
// live case this command exists for: Alice's recovery key lives inside
// a .docx saved as .txt).
func normalizeSecret(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// readFromTerminal prompts on stderr and reads one line from stdin with
// terminal echo disabled (golang.org/x/term), so the secret is never on
// the command line (shell history, `ps`) and never echoed to the screen.
// It refuses to run when stdin is not an interactive terminal -- silently
// falling back to a plaintext read would defeat the point -- naming the
// --*-stdin flag to use instead.
func readFromTerminal(stdin *os.File, stderr io.Writer, prompt string) (string, error) {
	if !term.IsTerminal(int(stdin.Fd())) {
		return "", fmt.Errorf("stdin is not a terminal; pipe the value in with --recovery-key-stdin (or --passphrase-stdin) instead")
	}
	fmt.Fprint(stderr, prompt)
	b, err := term.ReadPassword(int(stdin.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("read from terminal: %w", err)
	}
	return string(b), nil
}

// readSecretFromTerminal is readFromTerminal plus recovery-key whitespace
// normalization (see normalizeSecret).
func readSecretFromTerminal(stdin *os.File, stderr io.Writer, prompt string) (string, error) {
	raw, err := readFromTerminal(stdin, stderr, prompt)
	if err != nil {
		return "", err
	}
	return normalizeSecret(raw), nil
}

// readSecretFromStdin reads everything piped into stdin (e.g. from an
// extractor) and normalizes it the same way readSecretFromTerminal does.
func readSecretFromStdin(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return normalizeSecret(string(data)), nil
}

// cmdLink resolves <channel> <account> against cfg and runs that channel's
// link/login flow, relaying whatever the adapter writes (a QR code, an SSO
// URL) straight to stdout.
func cmdLink(ctx context.Context, cfg *config.Config, args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, linkUsage)
		return 2
	}
	channel, accountName := args[0], args[1]

	switch channel {
	case "whatsapp":
		acc, ok := findAccount(cfg, channel, accountName)
		if !ok {
			fmt.Fprintf(stderr, "error: no %q account named %q in config\n", channel, accountName)
			return 1
		}
		if err := whatsappLinkFunc(ctx, acc, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		return 0
	case "matrix":
		return cmdLinkMatrix(ctx, cfg, accountName, args[2:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown link channel %q (want whatsapp or matrix)\n", channel)
		return 2
	}
}

func cmdLinkMatrix(ctx context.Context, cfg *config.Config, accountName string, flagArgs []string, stdin *os.File, stdout, stderr io.Writer) int {
	fs := newFlagSet("link matrix", stderr)
	recoveryKey := fs.Bool("recovery-key", false, "import cross-signing keys and key backup; prompts for the recovery key on the terminal (no echo)")
	recoveryKeyStdin := fs.Bool("recovery-key-stdin", false, "import cross-signing keys and key backup, reading the recovery key from stdin (for piping from an extractor)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *recoveryKey && *recoveryKeyStdin {
		fmt.Fprintln(stderr, "error: --recovery-key and --recovery-key-stdin are mutually exclusive")
		return 2
	}

	acc, ok := findAccount(cfg, "matrix", accountName)
	if !ok {
		fmt.Fprintf(stderr, "error: no %q account named %q in config\n", "matrix", accountName)
		return 1
	}
	importing := *recoveryKey || *recoveryKeyStdin
	// Importing a key into an existing login must reuse it: a fresh SSO
	// mints a new device and orphans the crypto store bound to the old one.
	_, hasSession, err := matrix.LoadSession(matrix.StateDirFor(acc))
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if !(importing && hasSession) {
		if _, err := matrixLoginFunc(ctx, acc, stdout); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}

	if !importing {
		return 0
	}

	var key string
	if *recoveryKeyStdin {
		key, err = readSecretFromStdin(stdin)
	} else {
		key, err = readSecretFromTerminal(stdin, stderr, "Recovery key: ")
	}
	if err != nil {
		fmt.Fprintln(stderr, "error: read recovery key:", err)
		return 1
	}

	result, err := matrixImportRecoveryKeyFunc(ctx, acc, key)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(stdout, "restored cross-signing keys; restored %d/%d megolm sessions from the key backup\n", result.MegolmSessionsRestored, result.MegolmSessionsTotal)
	return 0
}

// findAccount returns the configured account named name on channel.
func findAccount(cfg *config.Config, channel, name string) (config.Account, bool) {
	for _, acc := range cfg.Accounts {
		if acc.Channel == channel && acc.Name == name {
			return acc, true
		}
	}
	return config.Account{}, false
}
