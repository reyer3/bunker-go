package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/openurl"
)

const meetingsUsage = `usage: bunker meetings [--days N] [--json]
       bunker meetings join <item-id|next> [--dry-run] [--json]`

// openMeetingURL opens a join link; tests replace it so no opener runs.
var openMeetingURL = func(rawURL string) error { return openurl.Open(rawURL, os.Getenv) }

// cmdMeetings lists the upcoming meetings (invitations in mail and recent
// call links), or joins one. Listing only reads the store. Joining opens
// the link on this machine, not the daemon's: the browser is the user's.
func cmdMeetings(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "join" {
		return cmdMeetingsJoin(ctx, backend, args[1:], stdout, stderr)
	}
	fs := newFlagSet("meetings", stderr)
	days := fs.Int("days", core.DefaultMeetingDays, "look this many days ahead (meetings in progress always show)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || *days < 1 {
		fmt.Fprintln(stderr, meetingsUsage)
		return 2
	}
	meetings, err := backend.Meetings(ctx, core.MeetingFilter{Days: *days})
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if meetings == nil {
			meetings = []core.UpcomingMeeting{}
		}
		writeJSON(stdout, map[string]any{"meetings": meetings})
		return 0
	}
	if len(meetings) == 0 {
		fmt.Fprintln(stdout, "no upcoming meetings")
		return 0
	}
	now := time.Now()
	for _, m := range meetings {
		fmt.Fprintln(stdout, formatMeeting(m, now))
	}
	return 0
}

// formatMeeting is one tab-separated record per line: when, title,
// provider, item id (what "meetings join" takes).
func formatMeeting(m core.UpcomingMeeting, now time.Time) string {
	when := "enlace"
	switch {
	case m.Link:
	case m.AllDay:
		when = m.Start.Local().Format("2006-01-02") + " todo el día"
	case m.InProgress(now):
		when = "ahora (hasta " + m.End.Local().Format("15:04") + ")"
	default:
		when = m.Start.Local().Format("2006-01-02 15:04")
	}
	provider := core.MeetingProvider(m.URL)
	if provider == "" {
		provider = "sin enlace"
	}
	title := strings.Join(strings.Fields(m.Summary), " ")
	return fmt.Sprintf("%s\t%s\t%s\t%s", when, title, provider, m.ItemID)
}

func cmdMeetingsJoin(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("meetings join", stderr)
	dryRun := fs.Bool("dry-run", false, "print the link and the command without opening anything")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, meetingsUsage)
		return 2
	}
	meeting, itemID, err := resolveMeeting(ctx, backend, positionals[0])
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	argv, err := openurl.Command(meeting.URL, os.Getenv)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("meetings: %s: %w", itemID, err))
	}
	if !*dryRun {
		if err := openMeetingURL(meeting.URL); err != nil {
			return fail(*jsonOut, stdout, stderr, fmt.Errorf("meetings: %w", err))
		}
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{
			"dryRun": *dryRun, "id": itemID, "summary": meeting.Summary,
			"url": meeting.URL, "command": argv,
		})
		return 0
	}
	if *dryRun {
		fmt.Fprintf(stdout, "[dry-run] would open %s with: %s\n", meeting.URL, strings.Join(argv, " "))
		return 0
	}
	fmt.Fprintf(stdout, "opening %s (%s)\n", meeting.URL, meeting.Summary)
	return 0
}

// resolveMeeting finds the meeting to join: "next" is the first one in
// the upcoming list that has a link, else the meeting item id names.
func resolveMeeting(ctx context.Context, backend Backend, id string) (core.Meeting, string, error) {
	if id == "next" {
		list, err := backend.Meetings(ctx, core.MeetingFilter{})
		if err != nil {
			return core.Meeting{}, "", err
		}
		for _, m := range list {
			if m.URL != "" {
				return m.Meeting, m.ItemID, nil
			}
		}
		return core.Meeting{}, "", errors.New("meetings: no upcoming meeting has a join link")
	}
	item, err := backend.Get(ctx, id)
	if err != nil {
		return core.Meeting{}, "", err
	}
	m, ok := core.MeetingFromItem(item)
	if !ok {
		return core.Meeting{}, "", fmt.Errorf("meetings: %s carries no meeting: %w", id, core.ErrNotFound)
	}
	return m, id, nil
}
