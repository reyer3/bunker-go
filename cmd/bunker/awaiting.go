package main

import (
	"context"
	"fmt"
	"io"

	"github.com/reyer3/bunker-go/internal/core"
)

const awaitingUsage = `usage: bunker awaiting [--days N] [--groups] [--mail] [--json]`

// cmdAwaiting lists the conversations awaiting a reply: where the user
// wrote last and nobody has answered for --days days. bunker derives the
// list from the store on every call; nothing is stored, so a reply makes
// a conversation drop off by itself. Mail threads count only when the
// user's last mail asks a question, unless --mail.
func cmdAwaiting(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("awaiting", stderr)
	days := fs.Int("days", core.DefaultAwaitingDays, "only conversations unanswered for at least this many days")
	groups := fs.Bool("groups", false, "include groups (left out by default)")
	mail := fs.Bool("mail", false, "include every mail thread, not only those whose last mail asks a question")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || *days < 1 {
		fmt.Fprintln(stderr, awaitingUsage)
		return 2
	}
	awaiting, err := backend.AwaitingReply(ctx, core.AwaitingFilter{Days: *days, Groups: *groups, Mail: *mail})
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if awaiting == nil {
			awaiting = []core.Awaiting{}
		}
		writeJSON(stdout, map[string]any{"awaiting": awaiting})
		return 0
	}
	if len(awaiting) == 0 {
		fmt.Fprintln(stdout, "no conversations awaiting a reply")
		return 0
	}
	for _, a := range awaiting {
		fmt.Fprintln(stdout, formatAwaiting(a))
	}
	return 0
}

// formatAwaiting is one tab-separated record per line: days waiting,
// when the last message was sent, person, its preview, item id.
func formatAwaiting(a core.Awaiting) string {
	return fmt.Sprintf("%d d\t%s\t%s\t%s\t%s", a.Days, a.Sent.Local().Format("2006-01-02 15:04"),
		a.Person, a.Preview, a.ItemID)
}
