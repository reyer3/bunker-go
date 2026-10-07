package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

const callUsage = `usage: bunker call <channel> <account> <to> [--dry-run] [--json]
       bunker call answer|reject|hangup <call-id|latest> [--dry-run] [--json]
       bunker call audio-test [--account A] [--seconds N] [--json]`

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
		action := core.CallAction(positionals[0])
		id := positionals[1]
		if id == "latest" {
			id, err = latestCallID(ctx, backend, action)
			if err != nil {
				return fail(*jsonOut, stdout, stderr, err)
			}
		}
		plan, call, err = backend.ControlCall(ctx, id, action, *dryRun)
	case len(positionals) == 3:
		channel, account := core.Channel(positionals[0]), positionals[1]
		var to string
		to, err = resolveRecipient(ctx, backend, channel, account, positionals[2], stderr, *jsonOut)
		if err != nil {
			return fail(*jsonOut, stdout, stderr, err)
		}
		plan, call, err = backend.PlaceCall(ctx, channel, account, to, *dryRun)
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

// latestCallID resolves "latest" for a call action, so a tmux key binding
// needs no id (issue #15): answer and reject pick the newest ringing
// incoming call, hangup the newest live call of any kind.
func latestCallID(ctx context.Context, backend Backend, action core.CallAction) (string, error) {
	calls, err := backend.Calls(ctx)
	if err != nil {
		return "", err
	}
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		ringing := c.Direction == core.CallIncoming && c.State == core.CallStateRinging
		if action == core.CallHangup && c.State != core.CallStateEnded || action != core.CallHangup && ringing {
			return c.ID, nil
		}
	}
	if action == core.CallHangup {
		return "", fmt.Errorf("no hay ninguna llamada en curso")
	}
	return "", fmt.Errorf("no hay ninguna llamada entrante sonando")
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
	direction := c.Direction
	if c.Video {
		direction += " video"
	}
	s := fmt.Sprintf("%s %s/%s %s %s %s", c.ID, c.Channel, c.Account, direction, peer, c.State)
	if !c.ConnectedAt.IsZero() {
		s += " " + core.FormatCallDuration(c.Duration(time.Now()))
	}
	if c.EndReason != "" {
		s += " (" + c.EndReason + ")"
	}
	if c.AudioError != "" {
		s += " [sin audio: " + c.AudioError + "]"
	}
	return s
}
