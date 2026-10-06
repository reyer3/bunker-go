package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/reyer3/bunker-go/internal/tui"
)

// "bunker herdr mailto" is the herdr plugin's "mailto" action, which its
// mailto: link handler runs on a Ctrl-click (deploy/herdr). herdr passes
// the clicked URL in HERDR_PLUGIN_CLICKED_URL; this opens the plugin's
// "compose" popup, which runs "bunker compose" on it.

const (
	herdrComposeEntrypoint = "compose"
	// herdrClickedURLEnv is set by herdr for a link handler's action.
	herdrClickedURLEnv = "HERDR_PLUGIN_CLICKED_URL"
)

// herdrComposeCommand opens the compose pane on url. The pane's placement
// (a popup) comes from the manifest; the URL reaches "bunker compose" as
// BUNKER_MAILTO because herdr runs a manifest pane's command as fixed argv.
func herdrComposeCommand(url string) []string {
	return []string{"plugin", "pane", "open", "--plugin", herdrPluginID, "--entrypoint", herdrComposeEntrypoint,
		"--env", mailtoEnv + "=" + url, "--focus"}
}

func cmdHerdrMailto(ctx context.Context, args []string, stdout, stderr io.Writer, deps herdrDeps) int {
	flags := flag.NewFlagSet("herdr mailto", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "print the herdr command instead of running it")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, herdrUsage)
		return 2
	}
	url := deps.getenv(herdrClickedURLEnv)
	if flags.NArg() == 1 {
		url = flags.Arg(0)
	}
	url = strings.TrimSpace(url)
	if url == "" {
		fmt.Fprintf(stderr, "error: herdr mailto: no URL (pass one or set %s)\n", herdrClickedURLEnv)
		return 2
	}
	// Validate before handing the URL to herdr, so a link bunker cannot
	// open fails here, in the action's log, instead of in an empty popup.
	if _, err := tui.ParseMailto(url); err != nil {
		fmt.Fprintln(stderr, "error: herdr mailto:", err)
		return 2
	}
	argv := herdrComposeCommand(url)
	if *dryRun {
		fmt.Fprintln(stdout, "herdr "+strings.Join(argv, " "))
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, herdrTimeout)
	defer cancel()
	if _, err := deps.run(ctx, argv...); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}
