package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// defaultSearchLimit is `bunker search`'s own --limit default, matching
// docs/cli.md's documented default of 50.
const defaultSearchLimit = 50

// cmdSearch implements `bunker search mail <account> [--from x]
// [--subject y] [--since D] [--before D] [--folder INBOX] [--limit 50]
// [--json]` (H3: mail-history). It prints results exactly like `list`
// does (same line format, same {"items": [...]} JSON shape), since a
// search hit and a synced item are the same core.Item.
func cmdSearch(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: bunker search mail <account> [--from x] [--subject y] [--since D] [--before D] [--folder INBOX] [--limit 50] [--json]"
	if len(args) < 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] != "mail" {
		fmt.Fprintf(stderr, "search: unsupported channel %q (only mail is supported)\n", args[0])
		return 2
	}

	fs := newFlagSet("search", stderr)
	from := fs.String("from", "", "match the From header")
	subject := fs.String("subject", "", "match the Subject header")
	since := fs.String("since", "", "match items on or after this date (YYYY-MM-DD)")
	before := fs.String("before", "", "match items strictly before this date (YYYY-MM-DD)")
	folder := fs.String("folder", "INBOX", "mailbox to search")
	limit := fs.Int("limit", defaultSearchLimit, "max results")
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

	criteria := core.SearchCriteria{Folder: *folder, From: *from, Subject: *subject, Limit: *limit}
	if *since != "" {
		t, err := time.Parse(backfillDateLayout, *since)
		if err != nil {
			fmt.Fprintf(stderr, "search: invalid --since %q: %v\n", *since, err)
			return 2
		}
		criteria.Since = t
	}
	if *before != "" {
		t, err := time.Parse(backfillDateLayout, *before)
		if err != nil {
			fmt.Fprintf(stderr, "search: invalid --before %q: %v\n", *before, err)
			return 2
		}
		criteria.Before = t
	}

	items, err := backend.Search(ctx, core.ChannelMail, positionals[0], criteria)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"items": items})
		return 0
	}
	for _, it := range items {
		mark := " "
		if it.Unread {
			mark = "*"
		}
		fmt.Fprintf(stdout, "%s %s\t%s\n", mark, it.ID, it.Subject)
	}
	return 0
}
