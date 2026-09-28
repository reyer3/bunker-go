package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// backfillDateLayout is the --since/--before date format: "YYYY-MM-DD",
// exactly as documented (docs/cli.md).
const backfillDateLayout = "2006-01-02"

// cmdBackfill implements `bunker backfill mail <account> --since
// YYYY-MM-DD [--folder INBOX] [--dry-run] [--json]` (H2: mail-history).
// Only "mail" is a supported channel today — the first positional names
// it explicitly (matching `link`/`import-keys`'s own channel-first
// shape) so a future channel's Backfiller slots in the same way without
// a flag rename.
func cmdBackfill(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: bunker backfill mail <account> --since YYYY-MM-DD [--folder INBOX] [--dry-run] [--json]"
	if len(args) < 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] != "mail" {
		fmt.Fprintf(stderr, "backfill: unsupported channel %q (only mail is supported)\n", args[0])
		return 2
	}

	fs := newFlagSet("backfill", stderr)
	since := fs.String("since", "", "backfill messages on or after this date (YYYY-MM-DD, required)")
	folder := fs.String("folder", "INBOX", "mailbox to search")
	dryRun := fs.Bool("dry-run", false, "report what would be added without upserting anything")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if *since == "" {
		fmt.Fprintln(stderr, "backfill: --since is required (YYYY-MM-DD)")
		return 2
	}
	sinceTime, err := time.Parse(backfillDateLayout, *since)
	if err != nil {
		fmt.Fprintf(stderr, "backfill: invalid --since %q: %v\n", *since, err)
		return 2
	}

	result, err := backend.Backfill(ctx, core.ChannelMail, positionals[0], *folder, sinceTime, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": *dryRun, "result": result})
		return 0
	}
	verb := "added"
	if *dryRun {
		verb = "would add"
	}
	if result.FirstID == "" {
		fmt.Fprintf(stdout, "%s 0 item(s); nothing found since %s\n", verb, *since)
		return 0
	}
	fmt.Fprintf(stdout, "%s %d item(s), range %s..%s\n", verb, result.Count, result.FirstID, result.LastID)
	return 0
}
