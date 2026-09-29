package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/reyer3/bunker-go/internal/core"
)

// bunker edit, delete and react (issues #76, #17) change a message
// already in a conversation. They follow send and reply: --dry-run
// prints the plan without touching the channel, --json prints the plan
// and receipt, and --idempotency-key makes a retry safe.

// stdinIsTerminal reports whether r is an interactive terminal: delete
// only asks for confirmation there, and refuses without --yes anywhere
// else (a script must opt in explicitly). A variable so tests can say.
var stdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// errDeleteNeedsYes is returned when delete cannot ask (stdin is not a
// terminal) and --yes was not given.
var errDeleteNeedsYes = errors.New("delete: refusing to delete without --yes when stdin is not a terminal")

func cmdEdit(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("edit", stderr)
	dryRun := fs.Bool("dry-run", false, "plan the edit without applying it")
	idemKey := fs.String("idempotency-key", "", "edit at most once per key: a repeat returns the first receipt")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 2 {
		fmt.Fprintln(stderr, "usage: bunker edit <id> <text|-> [--idempotency-key k] [--dry-run] [--json]")
		return 2
	}
	text, err := textOrStdin(positionals[1], stdin)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	plan, receipt, err := backend.EditMessage(core.WithIdempotencyKey(ctx, *idemKey), positionals[0], text, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printMessageAction(*jsonOut, *dryRun, plan, receipt, stdout)
}

func cmdDelete(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("delete", stderr)
	dryRun := fs.Bool("dry-run", false, "plan the delete without applying it")
	yes := fs.Bool("yes", false, "delete without asking (required when stdin is not a terminal)")
	idemKey := fs.String("idempotency-key", "", "delete at most once per key: a repeat returns the first receipt")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "usage: bunker delete <id> [--yes] [--idempotency-key k] [--dry-run] [--json]")
		return 2
	}
	id := positionals[0]
	// The plan comes first either way: it validates the request (our
	// message, within the window) and shows what is about to go.
	plan, _, err := backend.DeleteMessage(ctx, id, true)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *dryRun {
		return printMessageAction(*jsonOut, true, plan, core.Receipt{}, stdout)
	}
	if !*yes {
		if !stdinIsTerminal(stdin) {
			return fail(*jsonOut, stdout, stderr, errDeleteNeedsYes)
		}
		fmt.Fprintf(stderr, "Delete for everyone on %s/%s: %q? [y/N] ", plan.Channel, plan.Account, plan.Preview)
		answer, _ := bufio.NewReader(stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes", "s", "si", "sí":
		default:
			fmt.Fprintln(stderr, "not deleted")
			return 1
		}
	}
	plan, receipt, err := backend.DeleteMessage(core.WithIdempotencyKey(ctx, *idemKey), id, false)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printMessageAction(*jsonOut, false, plan, receipt, stdout)
}

func cmdReact(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("react", stderr)
	remove := fs.Bool("remove", false, "remove our reaction instead of setting one")
	dryRun := fs.Bool("dry-run", false, "plan the reaction without sending it")
	idemKey := fs.String("idempotency-key", "", "react at most once per key: a repeat returns the first receipt")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	usage := "usage: bunker react <id> <emoji|--remove> [--idempotency-key k] [--dry-run] [--json]"
	var emoji string
	switch {
	case *remove && len(positionals) == 1:
	case !*remove && len(positionals) == 2 && positionals[1] != "":
		emoji = positionals[1]
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	plan, receipt, err := backend.React(core.WithIdempotencyKey(ctx, *idemKey), positionals[0], emoji, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printMessageAction(*jsonOut, *dryRun, plan, receipt, stdout)
}

// printMessageAction renders an edit, delete or react plan (dry-run) or
// result. The JSON shape is send's: {dryRun, plan, receipt}.
func printMessageAction(jsonOut, dryRun bool, plan core.Plan, receipt core.Receipt, stdout io.Writer) int {
	if jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": dryRun, "plan": plan, "receipt": receipt})
		return 0
	}
	via := fmt.Sprintf("%s/%s", plan.Channel, plan.Account)
	var what string
	switch {
	case plan.Action == "edit":
		what = fmt.Sprintf("edit %s via %s to: %s", plan.Target, via, plan.Preview)
	case plan.Action == "delete":
		what = fmt.Sprintf("delete %s for everyone via %s: %q", plan.Target, via, plan.Preview)
	case plan.Action == "react" && plan.Preview == "":
		what = fmt.Sprintf("remove our reaction from %s via %s", plan.Target, via)
	default:
		what = fmt.Sprintf("react %s to %s via %s", plan.Preview, plan.Target, via)
	}
	if dryRun {
		fmt.Fprintf(stdout, "[dry-run] would %s\n", what)
		return 0
	}
	fmt.Fprintf(stdout, "%s ok: %s (receipt %s)\n", plan.Action, plan.Target, receipt.ID)
	if receipt.Replayed {
		fmt.Fprintln(stdout, "  already done with this idempotency key: nothing was sent again")
	}
	return 0
}
