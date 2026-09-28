package main

import (
	"context"
	"fmt"
	"io"
)

// cmdHealth reports every adapter's current health (R4): channel,
// account, connection state, since when, its last error (if any) and how
// many times it has been restarted.
func cmdHealth(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("health", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	adapters, err := backend.Health(ctx)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"adapters": adapters})
		return 0
	}
	if len(adapters) == 0 {
		fmt.Fprintln(stdout, "bunker: no adapters registered")
		return 0
	}
	for _, a := range adapters {
		line := fmt.Sprintf("%s/%s: %s (restarts=%d)", a.Channel, a.Account, a.State, a.Restarts)
		if a.LastError != "" {
			line += fmt.Sprintf(" last_error=%q", a.LastError)
		}
		fmt.Fprintln(stdout, line)
	}
	return 0
}
