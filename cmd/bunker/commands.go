package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// splitRecipients splits raw on commas, trims surrounding whitespace off
// each part and drops empty results, so "a@b.cl, c@d.cl ,," yields
// ["a@b.cl", "c@d.cl"]. Used for the comma-separated <to> positional and
// for each --cc value, which may itself be comma-separated.
func splitRecipients(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// collectCc splits and flattens every --cc flag value (each of which may
// itself be comma-separated) into one deduplicated-order recipient list.
func collectCc(raw []string) []string {
	var cc []string
	for _, v := range raw {
		cc = append(cc, splitRecipients(v)...)
	}
	return cc
}

// stringSliceFlag collects a repeatable flag (--label x --label y) into a
// slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

const topLevelUsage = `bunker <command> [flags]

Run without a command in a terminal to open the interactive UI.

Commands:
  daemon [--fake]                                          run the daemon
  version [--json]                                         this binary's version
                                                             (also --version)
  update [--dry-run] [--yes] [--json]                      install the latest
                                                             release after
                                                             checking its sha256
                                                             and restart the
                                                             daemon's service
  list [--channel c] [--account a] [--unread] [--label l]  list items, newest
       [-q text] [--query q] [--cursor c] [--limit n]        first; --query takes
       [--json]                                              the query language,
                                                             JSON has next_cursor
  find <query> [--cursor c] [--limit 50] [--json]          list --query shortcut
                                                             over the local store:
                                                             from: to: subject:
                                                             is: has: in: channel:
                                                             account: label:
                                                             before: after: "..." -x
  read <id> [--no-receipt] [--json]                        fetch full body
       (marks it read on WhatsApp/Matrix unless --no-receipt; mail is
       always PEEK-only, unaffected)
  reply <id> <text|-> [--cc addr]... [--attach path]...
       [--dry-run] [--json]                                 reply to an item
  send <channel> <account> <to> <text|->
       [--cc addr]... [--subject s]
       [--attach path]... [--media path]...
       [--dry-run] [--json]                                 send a fresh message
                                                             (<to> and each --cc
                                                             may be a comma list)
  organize <id> [--label x]... [--unlabel x]...
       [--move folder] [--seen|--unseen] [--dry-run] [--json]
  status post <channel> <account> <text>
       [--media path] [--dry-run] [--json]                 publish a status
  call <channel> <account> <to> [--dry-run] [--json]      place a voice call
                                                             (WhatsApp, opt-in;
                                                             audio plays on the
                                                             daemon's machine)
  call answer|reject|hangup <call-id|latest>
       [--dry-run] [--json]                                 control a live call
  calls [--json]                                           list live calls
  unread <id> [--json]                                     put an item back in
                                                             the unread inbox
  contacts [query] [--channel c] [--account a]
       [--limit n] [--json]                                 address books and
                                                             conversations; send
                                                             and call also take
                                                             a contact's name
  counts [--json]                                          unread counts
  health [--json]                                          per-adapter connection
                                                             state, since when,
                                                             last error, restarts
  download <id> [-n index] -o path [--force] [--json]      save an attachment
                                                             to disk (index
                                                             defaults to 0)
  avatar <channel> <account> <thread> [--json]              conversation avatar
                                                             PNG path (debug)
  thread <channel> <account> <thread>
       [--before RFC3339] [--limit N] [--json]              one conversation's
                                                             items, oldest→newest
  read-thread <channel> <account> <thread>
       [--no-receipt] [--json]                              mark every unread
                                                             item of one
                                                             conversation read
                                                             (not just the
                                                             newest); prints
                                                             how many it marked
  backfill mail <account> --since YYYY-MM-DD
       [--folder INBOX] [--dry-run] [--json]                 add mail the store
                                                             is missing since a
                                                             date, without
                                                             moving the sync
                                                             cursor
  search mail <account> [--from x] [--subject y]
       [--since D] [--before D] [--folder INBOX]
       [--limit 50] [--json]                                 server-side IMAP
                                                             search, upserting
                                                             hits (read-only)
  render [--tmux] [--json]                                 tmux status segment
  app [--dry-run]                                          open the TUI in its
                                                             own terminal window
  sidebar                                                  compact TUI for a
                                                             narrow pane (herdr)
  open [<id>]                                              TUI on one
                                                             conversation (id or
                                                             $BUNKER_OPEN_ID)
  herdr toggle [--dry-run] [--json]                        dock bunker as a left
                                                             panel in herdr, or
                                                             focus or close it
  mcp [--allow-send]                                       MCP server on stdio
                                                             for AI agents (sends
                                                             are plans unless
                                                             --allow-send)
  link whatsapp <account>                                  QR-pair WhatsApp
  link matrix <account> [--recovery-key|--recovery-key-stdin]
                                                             Matrix SSO login,
                                                             optionally importing
                                                             the recovery key
  import-keys matrix <account> <file> [--passphrase-stdin]  import an Element
                                                             megolm key export
`

// runWithBackend dispatches every command that talks to a Backend
// (everything except "daemon" and "render", which manage their own
// connection/fallback). It is the seam CLI command tests use with a
// fakeBackend, so they never open a real socket.
func runWithBackend(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, topLevelUsage)
		return 2
	}
	switch args[0] {
	case "list":
		return cmdList(ctx, backend, args[1:], stdout, stderr)
	case "find":
		return cmdFind(ctx, backend, args[1:], stdout, stderr)
	case "read":
		return cmdRead(ctx, backend, args[1:], stdout, stderr)
	case "reply":
		return cmdReply(ctx, backend, args[1:], stdin, stdout, stderr)
	case "send":
		return cmdSend(ctx, backend, args[1:], stdin, stdout, stderr)
	case "organize":
		return cmdOrganize(ctx, backend, args[1:], stdout, stderr)
	case "status":
		return cmdStatus(ctx, backend, args[1:], stdin, stdout, stderr)
	case "counts":
		return cmdCounts(ctx, backend, args[1:], stdout, stderr)
	case "download":
		return cmdDownload(ctx, backend, args[1:], stdout, stderr)
	case "avatar":
		return cmdAvatar(ctx, backend, args[1:], stdout, stderr)
	case "thread":
		return cmdThread(ctx, backend, args[1:], stdout, stderr)
	case "read-thread":
		return cmdReadThread(ctx, backend, args[1:], stdout, stderr)
	case "health":
		return cmdHealth(ctx, backend, args[1:], stdout, stderr)
	case "backfill":
		return cmdBackfill(ctx, backend, args[1:], stdout, stderr)
	case "search":
		return cmdSearch(ctx, backend, args[1:], stdout, stderr)
	case "call":
		return cmdCall(ctx, backend, args[1:], stdout, stderr)
	case "calls":
		return cmdCalls(ctx, backend, args[1:], stdout, stderr)
	case "contacts":
		return cmdContacts(ctx, backend, args[1:], stdout, stderr)
	case "unread":
		return cmdUnread(ctx, backend, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], topLevelUsage)
		return 2
	}
}

func writeJSON(w io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(w, `{"error":%q}`+"\n", err.Error())
		return
	}
	w.Write(b)
	w.Write([]byte("\n"))
}

func fail(jsonOut bool, stdout, stderr io.Writer, err error) int {
	if jsonOut {
		writeJSON(stdout, map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(stderr, "error:", err)
	}
	return 1
}

func textOrStdin(arg string, stdin io.Reader) (string, error) {
	if arg != "-" {
		return arg, nil
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseInterspersed parses fs against args the way every command in this
// CLI is documented (docs/cli.md): flags may appear before, after or
// between positional arguments, e.g. "reply <id> <text> --dry-run". The
// stdlib flag package alone stops at the first non-flag token, so this
// walks args once, routes anything starting with "-" (plus its value,
// unless it is a boolean flag or uses --flag=value) to fs.Parse, and
// returns the rest as positionals in their original order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var flagArgs, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			hasValue := strings.Contains(name, "=")
			if hasValue {
				name = name[:strings.Index(name, "=")]
			}
			fl := fs.Lookup(name)
			if fl == nil {
				return nil, fmt.Errorf("unknown flag: %s", a)
			}
			flagArgs = append(flagArgs, a)
			if !hasValue {
				type boolFlag interface{ IsBoolFlag() bool }
				bf, isBool := fl.Value.(boolFlag)
				if !isBool || !bf.IsBoolFlag() {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("flag %s needs a value", a)
					}
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
			continue
		}
		positionals = append(positionals, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	return positionals, nil
}

// defaultFindLimit is `bunker find`'s page size: find is for a person at
// a terminal, where one screenful plus a cursor beats an unbounded dump.
// `list` keeps its historical "0 = no limit" default.
const defaultFindLimit = 50

// listFlags are the flags `list` and `find` share.
type listFlags struct {
	channel, account, label, text, query, cursor *string
	unread, jsonOut                              *bool
	limit                                        *int
}

func newListFlags(fs *flag.FlagSet, defaultLimit int) listFlags {
	return listFlags{
		channel: fs.String("channel", "", "filter by channel (mail, whatsapp, matrix)"),
		account: fs.String("account", "", "filter by account"),
		unread:  fs.Bool("unread", false, "only unread items"),
		label:   fs.String("label", "", "filter by label"),
		text:    fs.String("q", "", "literal full-text search over subject, sender, recipients, body, attachment and thread names (no operators)"),
		query:   fs.String("query", "", "query language: from: to: subject: is: has: in: channel: account: label: before: after:, quotes and -negation (see docs/cli.md)"),
		cursor:  fs.String("cursor", "", "resume after a previous page's next_cursor"),
		limit:   fs.Int("limit", defaultLimit, "max items per page (0 = no limit)"),
		jsonOut: fs.Bool("json", false, "emit JSON"),
	}
}

func (f listFlags) filter() core.Filter {
	filter := core.Filter{
		Channel: core.Channel(*f.channel), Account: *f.account, Label: *f.label,
		Query: *f.text, Limit: *f.limit, Cursor: *f.cursor,
	}
	if *f.unread {
		t := true
		filter.Unread = &t
	}
	return filter
}

func cmdList(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("list", stderr)
	flags := newListFlags(fs, 0)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return printPage(ctx, backend, flags, *flags.query, stdout, stderr)
}

// cmdFind implements `bunker find <query...>`: `list --query` with the
// query as positionals (joined by spaces, so `bunker find from:ana
// factura` needs no quoting) and a page-sized default limit. It is not
// `bunker search`, which asks the mail server rather than the store.
func cmdFind(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("find", stderr)
	flags := newListFlags(fs, defaultFindLimit)
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	query := strings.Join(positionals, " ")
	if *flags.query != "" {
		query = strings.TrimSpace(*flags.query + " " + query)
	}
	if query == "" {
		fmt.Fprintln(stderr, `usage: bunker find <query> [--cursor C] [--limit 50] [--json]  (e.g. bunker find from:ana is:unread "orden de compra")`)
		return 2
	}
	return printPage(ctx, backend, flags, query, stdout, stderr)
}

// printPage runs one ListPage and prints it: {"items", "next_cursor"} as
// JSON, or one line per item with the cursor (when there is a next page)
// on stderr, so stdout stays exactly one item per line for scripts.
func printPage(ctx context.Context, backend Backend, flags listFlags, query string, stdout, stderr io.Writer) int {
	page, err := backend.ListPage(ctx, flags.filter(), query)
	if err != nil {
		return fail(*flags.jsonOut, stdout, stderr, err)
	}
	if *flags.jsonOut {
		items := page.Items
		if items == nil {
			items = []core.Item{}
		}
		writeJSON(stdout, map[string]any{"items": items, "next_cursor": page.NextCursor})
		return 0
	}
	for _, it := range page.Items {
		mark := " "
		if it.Unread {
			mark = "*"
		}
		fmt.Fprintf(stdout, "%s %s\t%s\n", mark, it.ID, it.Subject)
	}
	if page.NextCursor != "" {
		fmt.Fprintf(stderr, "next_cursor: %s\n", page.NextCursor)
	}
	return 0
}

func cmdRead(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("read", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	noReceipt := fs.Bool("no-receipt", false, "fetch without marking the item read (WhatsApp/Matrix)")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: bunker read <id> [--no-receipt] [--json]")
		return 2
	}
	item, err := backend.Read(ctx, positionals[0], !*noReceipt)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"item": item})
		return 0
	}
	fmt.Fprintf(stdout, "%s\nFrom: %s <%s>\nSubject: %s\n\n%s\n", item.ID, item.From.Name, item.From.ID, item.Subject, item.Body)
	return 0
}

func cmdReply(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("reply", stderr)
	var attach, cc stringSliceFlag
	fs.Var(&attach, "attach", "local file to attach (repeatable)")
	fs.Var(&cc, "cc", "additional recipient, comma-separated values allowed (repeatable)")
	dryRun := fs.Bool("dry-run", false, "plan the reply without sending it")
	idemKey := fs.String("idempotency-key", "", "send at most once per key: a repeat returns the first receipt")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 2 {
		fmt.Fprintln(stderr, "usage: bunker reply <id> <text|-> [--cc addr]... [--attach path]... [--idempotency-key k] [--dry-run] [--json]")
		return 2
	}
	if err := validateAttachmentPaths(attach); err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	body, err := textOrStdin(positionals[1], stdin)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	plan, receipt, err := backend.Reply(core.WithIdempotencyKey(ctx, *idemKey), positionals[0], body, collectCc(cc), attach, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printPlanResult(*jsonOut, *dryRun, plan, receipt, stdout)
}

// validateAttachmentPaths checks every path up front: it must exist, not
// be a directory, and be readable. That is the CLI's whole job — the MIME
// type and size rules that decide whether a channel will actually accept
// the file live in core.Service, validated against the adapter's own
// core.AttachmentPolicy (see internal/core/service.go), on --dry-run too.
// This runs before anything is sent or even planned, so a bad path fails
// fast with a clear error either way.
func validateAttachmentPaths(paths []string) error {
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return fmt.Errorf("attachment %q: %w", p, err)
		}
		info, statErr := f.Stat()
		f.Close()
		if statErr != nil {
			return fmt.Errorf("attachment %q: %w", p, statErr)
		}
		if info.IsDir() {
			return fmt.Errorf("attachment %q is a directory, not a file", p)
		}
	}
	return nil
}

func cmdSend(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("send", stderr)
	subject := fs.String("subject", "", "message subject (mail only)")
	var attach, media, cc stringSliceFlag
	fs.Var(&attach, "attach", "local file to attach (repeatable)")
	fs.Var(&media, "media", "alias of --attach, kept for compatibility (repeatable)")
	fs.Var(&cc, "cc", "additional recipient, comma-separated values allowed (repeatable)")
	dryRun := fs.Bool("dry-run", false, "plan the send without delivering it")
	idemKey := fs.String("idempotency-key", "", "send at most once per key: a repeat returns the first receipt")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 4 {
		fmt.Fprintln(stderr, "usage: bunker send <channel> <account> <to> <text|-> [--cc addr]... [--subject s] [--attach path]... [--media path]... [--idempotency-key k] [--dry-run] [--json]")
		return 2
	}
	attachments := append(append([]string{}, []string(media)...), []string(attach)...)
	if err := validateAttachmentPaths(attachments); err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	to := splitRecipients(positionals[2])
	if len(to) == 0 {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("no recipient given in %q", positionals[2]))
	}
	for i, recipient := range to {
		if to[i], err = resolveRecipient(ctx, backend, core.Channel(positionals[0]), positionals[1], recipient, stderr, *jsonOut); err != nil {
			return fail(*jsonOut, stdout, stderr, err)
		}
	}
	body, err := textOrStdin(positionals[3], stdin)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	out := core.Outgoing{
		Channel:     core.Channel(positionals[0]),
		Account:     positionals[1],
		To:          to,
		Cc:          collectCc(cc),
		Subject:     *subject,
		Body:        body,
		Attachments: attachments,
	}
	plan, receipt, err := backend.Send(core.WithIdempotencyKey(ctx, *idemKey), out, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printPlanResult(*jsonOut, *dryRun, plan, receipt, stdout)
}

func cmdOrganize(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("organize", stderr)
	var labels, unlabels stringSliceFlag
	fs.Var(&labels, "label", "add label (repeatable)")
	fs.Var(&unlabels, "unlabel", "remove label (repeatable)")
	move := fs.String("move", "", "move to folder")
	seen := fs.Bool("seen", false, "mark as read")
	unseen := fs.Bool("unseen", false, "mark as unread")
	dryRun := fs.Bool("dry-run", false, "plan the change without applying it")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 1 {
		fmt.Fprintln(stderr, "usage: bunker organize <id> [--label x]... [--unlabel x]... [--move folder] [--seen|--unseen] [--dry-run] [--json]")
		return 2
	}
	if *seen && *unseen {
		return fail(*jsonOut, stdout, stderr, errors.New("--seen and --unseen are mutually exclusive"))
	}

	op := core.OrganizeOp{AddLabels: labels, RemoveLabels: unlabels, MoveTo: *move}
	if *seen {
		t := true
		op.Seen = &t
	}
	if *unseen {
		f := false
		op.Seen = &f
	}

	plan, err := backend.Organize(ctx, positionals[0], op, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": *dryRun, "plan": plan})
		return 0
	}
	verb := "organized"
	if *dryRun {
		verb = "would organize"
	}
	fmt.Fprintf(stdout, "%s %s\n", verb, plan.Target)
	return 0
}

func cmdStatus(ctx context.Context, backend Backend, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "post" {
		fmt.Fprintln(stderr, "usage: bunker status post <channel> <account> <text> [--media path] [--dry-run] [--json]")
		return 2
	}
	fs := newFlagSet("status post", stderr)
	media := fs.String("media", "", "local media file path")
	dryRun := fs.Bool("dry-run", false, "plan the post without publishing it")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 3 {
		fmt.Fprintln(stderr, "usage: bunker status post <channel> <account> <text> [--media path] [--dry-run] [--json]")
		return 2
	}
	text, err := textOrStdin(positionals[2], stdin)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	status := core.Status{Text: text, Media: *media}
	plan, receipt, err := backend.PostStatus(ctx, core.Channel(positionals[0]), positionals[1], status, *dryRun)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	return printPlanResult(*jsonOut, *dryRun, plan, receipt, stdout)
}

func cmdCounts(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("counts", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	counts, err := backend.Counts(ctx)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"counts": counts})
		return 0
	}
	for channel, byAccount := range counts {
		for account, n := range byAccount {
			fmt.Fprintf(stdout, "%s/%s: %d\n", channel, account, n)
		}
	}
	return 0
}

// cmdDownload saves one attachment of a stored item to disk. -n selects
// which attachment when an item carries more than one (default 0); -o is
// required (there is no default output path — the CLI never guesses
// where Alice or Claude Code wants a file written). The backend (daemon
// over RPC, or an in-process core.Service) does the actual write, so this
// function only builds the call and renders its result.
func cmdDownload(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("download", stderr)
	index := fs.Int("n", 0, "attachment index (default 0)")
	out := fs.String("o", "", "output file path (required)")
	force := fs.Bool("force", false, "overwrite an existing file at the output path")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 1 || *out == "" {
		fmt.Fprintln(stderr, "usage: bunker download <id> [-n index] -o path [--force] [--json]")
		return 2
	}

	// The daemon writes the file from its own working directory, so a
	// relative -o is resolved here, against the caller's.
	dest, err := filepath.Abs(*out)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("resolve output path: %w", err))
	}

	res, err := backend.Download(ctx, positionals[0], *index, dest, core.DownloadOptions{Force: *force})
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"result": res})
		return 0
	}
	fmt.Fprintf(stdout, "saved %s (%s, %d bytes) to %s\n", res.Name, res.MIME, res.Bytes, res.Path)
	return 0
}

// cmdAvatar prints the local PNG path for one conversation's avatar,
// fetching and caching it (or generating the brand-color/initial
// fallback) through the backend exactly as V4's TUI will. It exists as a
// debugging/scripting entry point ahead of that TUI work landing.
func cmdAvatar(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("avatar", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 3 {
		fmt.Fprintln(stderr, "usage: bunker avatar <channel> <account> <thread> [--json]")
		return 2
	}

	res, err := backend.Avatar(ctx, core.Channel(positionals[0]), positionals[1], positionals[2])
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"result": res})
		return 0
	}
	fmt.Fprintln(stdout, res.Path)
	return 0
}

// cmdThread prints one conversation's items, oldest→newest: the same
// (channel, account, thread) triple avatar uses, plus a --before RFC3339
// cursor for scrolling up (older messages) and --limit (the daemon
// applies its own default when omitted/zero, so this never sends a
// synthetic default of its own).
func cmdThread(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("thread", stderr)
	before := fs.String("before", "", "RFC3339 timestamp cursor: return items strictly before it (default: newest)")
	limit := fs.Int("limit", 0, "max items to return (default: the daemon's own default, currently 50)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 3 {
		fmt.Fprintln(stderr, "usage: bunker thread <channel> <account> <thread> [--before RFC3339] [--limit N] [--json]")
		return 2
	}

	var beforeTime time.Time
	if *before != "" {
		beforeTime, err = time.Parse(time.RFC3339, *before)
		if err != nil {
			fmt.Fprintf(stderr, "invalid --before %q: %v\n", *before, err)
			return 2
		}
	}

	items, err := backend.Thread(ctx, positionals[0], positionals[1], positionals[2], beforeTime, *limit)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"items": items})
		return 0
	}
	for _, it := range items {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", it.ID, it.Timestamp.Format(time.RFC3339), it.Body)
	}
	return 0
}

// cmdReadThread marks every unread, non-FromMe item of one conversation
// read (see core.Service.ReadThread) — the fix for the live bug where
// opening a conversation in the TUI marked only its newest item read,
// leaving older unread items stranded in the unread panel
// (conversation-view.md, K9). --no-receipt still clears the local unread
// state but skips notifying the channel itself (no WhatsApp/Matrix
// receipts, no mail \Seen).
func cmdReadThread(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("read-thread", stderr)
	noReceipt := fs.Bool("no-receipt", false, "mark read locally without notifying the channel (no receipts/\\Seen)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) < 3 {
		fmt.Fprintln(stderr, "usage: bunker read-thread <channel> <account> <thread> [--no-receipt] [--json]")
		return 2
	}

	count, err := backend.ReadThread(ctx, positionals[0], positionals[1], positionals[2], !*noReceipt)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"count": count})
		return 0
	}
	fmt.Fprintf(stdout, "%d item(s) marked read\n", count)
	return 0
}

// printPlanResult renders the shared {dryRun, plan, receipt} shape used by
// reply, send and status post. A fan-out send/reply (T13a: Plan.Recipients
// has more than one address on an adapter without
// core.MultiRecipientSender) is delegated to printFanoutResult instead,
// since it needs a per-recipient breakdown and its own exit code; reply
// and status never reach that path (their Recipients has at most one
// entry).
func printPlanResult(jsonOut, dryRun bool, plan core.Plan, receipt core.Receipt, stdout io.Writer) int {
	if len(plan.Recipients) > 1 {
		return printFanoutResult(jsonOut, dryRun, plan, receipt, stdout)
	}
	if jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": dryRun, "plan": plan, "receipt": receipt})
		return 0
	}
	to := ""
	if len(plan.Recipients) > 0 {
		to = fmt.Sprintf(" to %v", plan.Recipients)
	}
	cc := ""
	if len(plan.Cc) > 0 {
		cc = fmt.Sprintf(" cc %v", plan.Cc)
	}
	subject := ""
	if plan.Subject != "" {
		subject = fmt.Sprintf(" subject %q", plan.Subject)
	}
	if dryRun {
		fmt.Fprintf(stdout, "[dry-run] would %s via %s/%s%s%s%s: %s\n", plan.Action, plan.Channel, plan.Account, to, cc, subject, plan.Preview)
	} else {
		fmt.Fprintf(stdout, "%s ok: %s%s (receipt %s)\n", plan.Action, plan.Target, cc, receipt.ID)
		if receipt.Replayed {
			fmt.Fprintln(stdout, "  already sent with this idempotency key: nothing was sent again")
		}
	}
	for _, att := range plan.Attachments {
		fmt.Fprintf(stdout, "  %s (%s, %d bytes)\n", att.Name, att.MIME, att.Size)
	}
	return 0
}

// printFanoutResult renders a fan-out send (T13a): dry-run shows the full
// per-recipient plan and the estimated pause budget without sending or
// sleeping anything; a real send lists each recipient's ✓/✗ outcome and
// exits 1 if any recipient failed, since "one failure never stops the
// rest" must never look like a clean success.
func printFanoutResult(jsonOut, dryRun bool, plan core.Plan, receipt core.Receipt, stdout io.Writer) int {
	exitCode := 0
	if !dryRun {
		for _, r := range receipt.Recipients {
			if r.Error != "" {
				exitCode = 1
				break
			}
		}
	}

	if jsonOut {
		writeJSON(stdout, map[string]any{"dryRun": dryRun, "plan": plan, "receipt": receipt})
		return exitCode
	}

	cc := ""
	if len(plan.Cc) > 0 {
		cc = fmt.Sprintf(" cc %v", plan.Cc)
	}
	subject := ""
	if plan.Subject != "" {
		subject = fmt.Sprintf(" subject %q", plan.Subject)
	}

	if dryRun {
		fmt.Fprintf(stdout, "[dry-run] would %s via %s/%s to %d recipients %v%s%s: %s\n",
			plan.Action, plan.Channel, plan.Account, len(plan.Recipients), plan.Recipients, cc, subject, plan.Preview)
		if plan.FanoutPauseMax > 0 {
			fmt.Fprintf(stdout, "  estimated pause between recipients: %s-%s (plus each adapter's own composing/typing time)\n",
				plan.FanoutPauseMin.Round(time.Second), plan.FanoutPauseMax.Round(time.Second))
		}
		for _, att := range plan.Attachments {
			fmt.Fprintf(stdout, "  %s (%s, %d bytes)\n", att.Name, att.MIME, att.Size)
		}
		return 0
	}

	fmt.Fprintf(stdout, "%s: %d recipients%s\n", plan.Action, len(receipt.Recipients), cc)
	for _, r := range receipt.Recipients {
		if r.Error == "" {
			fmt.Fprintf(stdout, "  ✓ %s (receipt %s)\n", r.To, r.Receipt.ID)
		} else {
			fmt.Fprintf(stdout, "  ✗ %s: %s\n", r.To, r.Error)
		}
	}
	return exitCode
}

// cmdUnread puts an item back in the unread inbox. It is not an outbound
// action (nothing is sent to anyone), so it has no --dry-run.
func cmdUnread(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("unread", stderr)
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintln(stderr, "usage: bunker unread <id> [--json]")
		return 2
	}
	local, err := backend.MarkUnread(ctx, positionals[0])
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		writeJSON(stdout, map[string]any{"id": positionals[0], "local_only": local})
		return 0
	}
	if local {
		fmt.Fprintf(stdout, "unread in bunker only (the channel cannot mark it unread): %s\n", positionals[0])
		return 0
	}
	fmt.Fprintf(stdout, "unread: %s\n", positionals[0])
	return 0
}
