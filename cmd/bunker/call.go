package main

import (
	"context"
	"fmt"
	"io"

	"github.com/reyer3/bunker-go/internal/core"
)

const callUsage = `usage: bunker call <channel> <account> <to> [--dry-run] [--json]
       bunker call answer|reject|hangup <call-id> [--dry-run] [--json]`

// cmdCall places a voice call, or answers/rejects/hangs up a live one.
// Audio runs on the machine the daemon runs on (its microphone and
// speaker), never in the CLI process: the command returns as soon as the
// daemon has acted, and "bunker calls" shows how the call is going.
func cmdCall(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("call", stderr)
	dryRun := fs.Bool("dry-run", false, "plan the call action without touching the network")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	var (
		plan core.Plan
		call core.Call
	)
	switch {
	case len(positionals) == 2 && isCallAction(positionals[0]):
		plan, call, err = backend.ControlCall(ctx, positionals[1], core.CallAction(positionals[0]), *dryRun)
	case len(positionals) == 3:
		plan, call, err = backend.PlaceCall(ctx, core.Channel(positionals[0]), positionals[1], positionals[2], *dryRun)
	default:
		fmt.Fprintln(stderr, callUsage)
		return 2
	}
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": *dryRun, "plan": plan, "call": call})
		return 0
	}
	if *dryRun {
		fmt.Fprintf(stdout, "[dry-run] would %s via %s/%s: %s\n", plan.Action, plan.Channel, plan.Account, plan.Target)
		return 0
	}
	fmt.Fprintf(stdout, "%s ok: %s\n", plan.Action, formatCall(call))
	return 0
}

// cmdCalls lists every live call.
func cmdCalls(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("calls", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	calls, err := backend.Calls(ctx)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if calls == nil {
			calls = []core.Call{}
		}
		writeJSON(stdout, map[string]any{"calls": calls})
		return 0
	}
	if len(calls) == 0 {
		fmt.Fprintln(stdout, "no active calls")
		return 0
	}
	for _, c := range calls {
		fmt.Fprintln(stdout, formatCall(c))
	}
	return 0
}

func isCallAction(s string) bool {
	switch core.CallAction(s) {
	case core.CallAnswer, core.CallReject, core.CallHangup:
		return true
	}
	return false
}

// formatCall renders one call as "<id> <channel>/<account> <direction>
// <peer> [name] <state>[ (reason)]".
func formatCall(c core.Call) string {
	peer := c.Peer
	if c.PeerName != "" {
		peer = fmt.Sprintf("%s (%s)", c.PeerName, c.Peer)
	}
	s := fmt.Sprintf("%s %s/%s %s %s %s", c.ID, c.Channel, c.Account, c.Direction, peer, c.State)
	if c.EndReason != "" {
		s += " (" + c.EndReason + ")"
	}
	return s
}
