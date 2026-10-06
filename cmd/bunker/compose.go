package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/reyer3/bunker-go/internal/tui"
)

// "bunker compose" opens the TUI's new mail editor prefilled from a
// mailto: URL and quits when the editor closes. It is the herdr plugin's
// "compose" pane (deploy/herdr), which "bunker herdr mailto" opens when a
// mailto: link is Ctrl-clicked in herdr, and works in any terminal too.

const (
	composeUsage = "usage: bunker compose [--account <name>] <mailto-url>   (or BUNKER_MAILTO=<mailto-url> bunker compose)"
	// mailtoEnv carries the URL into "bunker compose" when herdr starts
	// it: a manifest pane's command is fixed argv, so the URL travels
	// through "--env", as BUNKER_OPEN_ID does for "bunker open".
	mailtoEnv = "BUNKER_MAILTO"
)

func cmdCompose(args []string, stdin *os.File, stdout, stderr io.Writer, deps runDependencies) int {
	if isHelpArg(args) {
		fmt.Fprintln(stdout, composeUsage)
		return 0
	}
	flags := flag.NewFlagSet("compose", flag.ContinueOnError)
	flags.SetOutput(stderr)
	account := flags.String("account", "", "mail account to send from (default: the first mail account in config.toml)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, composeUsage)
		return 2
	}
	raw := deps.env(mailtoEnv)
	if flags.NArg() == 1 {
		raw = flags.Arg(0)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		fmt.Fprintln(stderr, composeUsage)
		return 2
	}
	draft, err := tui.ParseMailto(raw)
	if err != nil {
		fmt.Fprintln(stderr, "error: compose:", err)
		return 2
	}
	name, err := composeAccount(deps, *account)
	if err != nil {
		fmt.Fprintln(stderr, "error: compose:", err)
		return 2
	}
	if !deps.isTerminal(stdin) {
		fmt.Fprintln(stderr, "error: bunker compose needs a terminal")
		return 2
	}
	return runTUI(stdin, stdout, stderr, deps, tuiLaunch{mailDraft: &draft, mailAccount: name})
}

// composeAccount picks the mail account to send from: want when it is a
// configured mail account, else the first one in config.toml.
func composeAccount(deps runDependencies, want string) (string, error) {
	if deps.loadConfig == nil {
		return "", errors.New("no mail account configured")
	}
	cfg, err := deps.loadConfig()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	for _, acc := range cfg.Accounts {
		if acc.Channel == "mail" && (want == "" || acc.Name == want) {
			return acc.Name, nil
		}
	}
	if want != "" {
		return "", fmt.Errorf("no mail account %q in config.toml", want)
	}
	return "", errors.New("no mail account configured in config.toml")
}
