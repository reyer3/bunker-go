package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// "bunker sidebar" and "bunker open" (issue #81) are the two panes of the
// herdr plugin (deploy/herdr): a compact list, and one conversation opened
// from it in a pane of its own. Both are the TUI started in another mode,
// with the same terminal and daemon requirements as bare "bunker".

const (
	openUsage    = "usage: bunker open [<item-id>]   (or BUNKER_OPEN_ID=<item-id> bunker open)"
	sidebarUsage = "usage: bunker sidebar"
	// openIDEnv carries the item id into "bunker open" when herdr starts
	// it: herdr runs a manifest pane's command as fixed argv, so the id
	// cannot be an argument and travels through "--env" instead.
	openIDEnv = "BUNKER_OPEN_ID"
)

// validItemID rejects an item id that could be read as a flag or break
// the argv or environment it is put in. Ids come from the daemon, but
// herdr receives them through its command line.
func validItemID(id string) error {
	if id == "" {
		return errors.New("empty item id")
	}
	if strings.HasPrefix(id, "-") {
		return fmt.Errorf("invalid item id %q: starts with -", id)
	}
	if len(id) > 1024 {
		return fmt.Errorf("invalid item id: %d bytes, too long", len(id))
	}
	for _, r := range id {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid item id %q: control character", id)
		}
	}
	return nil
}

func isHelpArg(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

func cmdOpen(args []string, stdin *os.File, stdout, stderr io.Writer, deps runDependencies) int {
	if isHelpArg(args) {
		fmt.Fprintln(stdout, openUsage)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintln(stderr, openUsage)
		return 2
	}
	id := deps.env(openIDEnv)
	if len(args) == 1 {
		id = args[0]
	}
	id = strings.TrimSpace(id)
	if id == "" {
		fmt.Fprintln(stderr, openUsage)
		return 2
	}
	if err := validItemID(id); err != nil {
		fmt.Fprintln(stderr, "error: open:", err)
		return 2
	}
	if !deps.isTerminal(stdin) {
		fmt.Fprintln(stderr, "error: bunker open needs a terminal")
		return 2
	}
	return runTUI(stdin, stdout, stderr, deps, tuiLaunch{openID: id})
}

func cmdSidebar(args []string, stdin *os.File, stdout, stderr io.Writer, deps runDependencies) int {
	if isHelpArg(args) {
		fmt.Fprintln(stdout, sidebarUsage)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(stderr, sidebarUsage)
		return 2
	}
	if !deps.isTerminal(stdin) {
		fmt.Fprintln(stderr, "error: bunker sidebar needs a terminal")
		return 2
	}
	return runTUI(stdin, stdout, stderr, deps, tuiLaunch{sidebar: true, opener: herdrItemOpener(deps.env, deps.herdrRun)})
}
