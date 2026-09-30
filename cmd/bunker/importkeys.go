package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/reyer3/bunker-go/internal/channel/matrix"
	"github.com/reyer3/bunker-go/internal/config"
)

// importKeysUsage documents `bunker import-keys`, which -- like `bunker
// link` -- drives an adapter's own key-import entry point directly
// against the account's existing state, never through the daemon.
const importKeysUsage = `bunker import-keys <channel> <account> <file> [flags]

Channels:
  matrix <account> <file> [--passphrase-stdin] [--json]
                                                  import an Element megolm
                                                  key export; prompts for
                                                  the passphrase on the
                                                  terminal (no echo)
`

// matrixImportKeyExportFunc is internal/channel/matrix.ImportKeyExportForAccount,
// held as a package var so tests can substitute a fake: this command must
// never dial a real Matrix account.
var matrixImportKeyExportFunc = matrix.ImportKeyExportForAccount // returns matrix.ImportKeyResult

// cmdImportKeys resolves <channel> <account> <file> against cfg and runs
// that channel's key-import flow.
func cmdImportKeys(ctx context.Context, cfg *config.Config, args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprint(stderr, importKeysUsage)
		return 2
	}
	channel, accountName, file := args[0], args[1], args[2]

	switch channel {
	case "matrix":
		return cmdImportKeysMatrix(ctx, cfg, accountName, file, args[3:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown import-keys channel %q (want matrix)\n", channel)
		return 2
	}
}

func cmdImportKeysMatrix(ctx context.Context, cfg *config.Config, accountName, file string, flagArgs []string, stdin *os.File, stdout, stderr io.Writer) int {
	fs := newFlagSet("import-keys matrix", stderr)
	passphraseStdin := fs.Bool("passphrase-stdin", false, "read the export passphrase from stdin instead of prompting on the terminal")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	acc, ok := findAccount(cfg, "matrix", accountName)
	if !ok {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("no %q account named %q in config", "matrix", accountName))
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("read key export file: %w", err))
	}

	var passphrase string
	if *passphraseStdin {
		passphrase, err = readLineFromStdin(stdin)
	} else {
		passphrase, err = readPassphraseFromTerminal(stdin, stderr, "Export passphrase: ")
	}
	if err != nil {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("read passphrase: %w", err))
	}

	result, err := matrixImportKeyExportFunc(ctx, acc, passphrase, data)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"new": result.New, "already_known": result.AlreadyKnown, "failed": result.Failed, "total": result.Total})
		return 0
	}
	fmt.Fprintf(stdout, "imported %d new, %d already known", result.New, result.AlreadyKnown)
	if result.Failed > 0 {
		fmt.Fprintf(stdout, ", %d failed", result.Failed)
	}
	fmt.Fprintf(stdout, " (%d in export) from %s\n", result.Total, file)
	return 0
}

// readLineFromStdin reads everything piped into stdin and trims a single
// trailing line ending, keeping any other whitespace: unlike a recovery
// key, a passphrase may legitimately contain internal spaces.
func readLineFromStdin(stdin io.Reader) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	s := string(data)
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s, nil
}

// readPassphraseFromTerminal is readFromTerminal without recovery-key-style
// whitespace normalization: a passphrase's internal spaces are meaningful.
func readPassphraseFromTerminal(stdin *os.File, stderr io.Writer, prompt string) (string, error) {
	return readFromTerminal(stdin, stderr, prompt)
}
