package main

import (
	"context"
	"fmt"
	"io"
)

// cmdHealth reports every adapter's current health (R4): channel,
// account, connection state, since when, its last error (if any) and how
// many times it has been restarted; and whether the daemon has seen a
// newer bunker release (issue #105).
func cmdHealth(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("health", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	report, err := backend.HealthReport(ctx)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	adapters := report.Adapters
	if *jsonOut {
		out := map[string]any{"adapters": adapters, "update_available": report.Update.Available}
		if report.Update.Latest != "" {
			out["latest_version"] = report.Update.Latest
		}
		writeJSON(stdout, out)
		return 0
	}
	if len(adapters) == 0 {
		fmt.Fprintln(stdout, "bunker: no adapters registered")
	}
	for _, a := range adapters {
		line := fmt.Sprintf("%s/%s: %s (restarts=%d)", a.Channel, a.Account, a.State, a.Restarts)
		if a.LastError != "" {
			line += fmt.Sprintf(" last_error=%q", a.LastError)
		}
		fmt.Fprintln(stdout, line)
	}
	if report.Update.Available {
		fmt.Fprintf(stdout, "bunker: new version v%s available, run 'bunker update'\n", report.Update.Latest)
	}
	return 0
}
