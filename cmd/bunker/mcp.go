package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// "bunker mcp" (issue #31) serves bunker to AI agents (Claude Code, Zed's
// agent panel, any MCP host) over stdio. Every tool is one daemon RPC on
// a fresh connection, so the server outlives daemon restarts. Reads
// never mark anything read. Sends and replies only return the dry-run
// plan unless the server was started with --allow-send AND the call
// sets confirm: the agent can draft freely, but a real message needs
// the person to have opted in, and the daemon's pacing still applies.

const mcpInstructions = `bunker is the user's inbox: mail, WhatsApp and Matrix in one store.
Use counts and list to see what is new, read or thread to open it (reading never marks anything read),
and contacts to find someone's address by name. health says whether the daemon and each account are connected. send and reply return a plan (dry run) unless
confirm is true; confirming only works when the user started the server with --allow-send.
A confirmed send that timed out is safe to retry with the same arguments: it is never sent twice
(the receipt then says replayed). Never send a message the user did not ask for.`

// mcpBodyLimit caps a message body in tool results: an agent needs the
// text, not megabytes of quoted mail history.
const (
	mcpBodyLimit    = 20000
	mcpSnippetLimit = 200
	mcpListDefault  = 20
	mcpListMax      = 100
	mcpCallTimeout  = 60 * time.Second
)

// mcpDialer opens a Backend for one tool call.
type mcpDialer func(ctx context.Context) (Backend, io.Closer, error)

func dialDaemonBackend(ctx context.Context) (Backend, io.Closer, error) {
	client, err := rpc.DialContext(ctx, rpc.DefaultSocketPath())
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach bunker daemon at %s: %w", rpc.DefaultSocketPath(), err)
	}
	return client, client, nil
}

func cmdMCP(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allowSend := fs.Bool("allow-send", false, "let tools send for real when a call sets confirm (default: plans only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "usage: bunker mcp [--allow-send]")
		return 2
	}
	server := newMCPServer(dialDaemonBackend, *allowSend)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		fmt.Fprintln(stderr, "error: mcp:", err)
		return 1
	}
	return 0
}

// withBackend runs fn against a fresh daemon connection, with the read
// timeout (sends use mcpSendTimeout, see mcpOutbound).
func withBackend[T any](ctx context.Context, dial mcpDialer, fn func(context.Context, Backend) (T, error)) (T, error) {
	return withBackendTimeout(ctx, dial, mcpCallTimeout, fn)
}

// mcpItem is an item as tools return it: the body is a snippet in
// listings and the (capped) full text when reading.
type mcpItem struct {
	ID          string   `json:"id"`
	Channel     string   `json:"channel"`
	Account     string   `json:"account"`
	Thread      string   `json:"thread,omitempty"`
	ThreadName  string   `json:"thread_name,omitempty"`
	From        string   `json:"from"`
	FromMe      bool     `json:"from_me,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	Body        string   `json:"body,omitempty"`
	Truncated   bool     `json:"truncated,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Unread      bool     `json:"unread,omitempty"`
	Time        string   `json:"time"`
}

func toMCPItem(it core.Item, bodyLimit int) mcpItem {
	from := it.From.Name
	if from == "" {
		from = it.From.ID
	} else if it.From.ID != "" && it.From.ID != from {
		from += " <" + it.From.ID + ">"
	}
	body, truncated := truncateRunes(it.Body, bodyLimit)
	out := mcpItem{
		ID: it.ID, Channel: string(it.Channel), Account: it.Account, Thread: it.Thread, ThreadName: it.ThreadName,
		From: from, FromMe: it.FromMe, Subject: it.Subject, Body: body, Truncated: truncated,
		Unread: it.Unread, Time: it.Timestamp.Format(time.RFC3339),
	}
	if it.Deleted {
		out.Body = "(mensaje eliminado)"
	}
	for _, a := range it.Attachments {
		out.Attachments = append(out.Attachments, a.Name)
	}
	return out
}

func truncateRunes(s string, limit int) (string, bool) {
	if utf8.RuneCountInString(s) <= limit {
		return s, false
	}
	r := []rune(s)
	return string(r[:limit]) + "…", true
}

func toMCPItems(items []core.Item, bodyLimit int) []mcpItem {
	out := make([]mcpItem, 0, len(items))
	for _, it := range items {
		out = append(out, toMCPItem(it, bodyLimit))
	}
	return out
}

type (
	mcpNoInput   struct{}
	mcpCountsOut struct {
		Unread map[core.Channel]map[string]int `json:"unread" jsonschema:"unread count per channel and account"`
	}
	mcpListIn struct {
		Channel string `json:"channel,omitempty" jsonschema:"mail, whatsapp or matrix; empty for every channel"`
		Account string `json:"account,omitempty" jsonschema:"account name; empty for every account"`
		Unread  bool   `json:"unread,omitempty" jsonschema:"only unread items"`
		Limit   int    `json:"limit,omitempty" jsonschema:"at most this many items (default 20, max 100)"`
	}
	mcpItemsOut struct {
		Items []mcpItem `json:"items"`
	}
	mcpReadIn struct {
		ID string `json:"id" jsonschema:"the item id, as list returns it"`
	}
	mcpThreadIn struct {
		Channel string `json:"channel" jsonschema:"mail, whatsapp or matrix"`
		Account string `json:"account" jsonschema:"account name"`
		Thread  string `json:"thread" jsonschema:"the thread id, as list or contacts return it"`
		Limit   int    `json:"limit,omitempty" jsonschema:"at most this many of the newest messages (default 20, max 100)"`
	}
	mcpContactsIn struct {
		Query   string `json:"query,omitempty" jsonschema:"name or address to match, ignoring case and accents"`
		Channel string `json:"channel,omitempty" jsonschema:"only this channel"`
		Account string `json:"account,omitempty" jsonschema:"only this account"`
		Limit   int    `json:"limit,omitempty" jsonschema:"at most this many (default 20, max 100)"`
	}
	mcpContactsOut struct {
		Contacts []core.Contact `json:"contacts"`
	}
	mcpCallsOut struct {
		Calls []core.Call `json:"calls"`
	}
	mcpSendIn struct {
		Channel string `json:"channel" jsonschema:"mail, whatsapp or matrix"`
		Account string `json:"account" jsonschema:"account to send from"`
		To      string `json:"to" jsonschema:"an address, or a contact name resolved within that account"`
		Text    string `json:"text" jsonschema:"the message"`
		Subject string `json:"subject,omitempty" jsonschema:"mail only"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true sends, and only works when the server runs with --allow-send"`
	}
	mcpReplyIn struct {
		ID      string `json:"id" jsonschema:"the item to reply to"`
		Text    string `json:"text" jsonschema:"the reply"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true sends, and only works when the server runs with --allow-send"`
	}
	mcpHealthOut struct {
		DaemonUp bool               `json:"daemon_up" jsonschema:"whether the bunker daemon answered"`
		Error    string             `json:"error,omitempty" jsonschema:"why the daemon could not be reached"`
		Hint     string             `json:"hint,omitempty" jsonschema:"what the user can do about it"`
		Adapters []mcpAdapterHealth `json:"adapters" jsonschema:"one entry per configured account"`
	}
	mcpAdapterHealth struct {
		Channel   string `json:"channel"`
		Account   string `json:"account"`
		State     string `json:"state" jsonschema:"connecting, connected, backoff or stopped"`
		Since     string `json:"since,omitempty" jsonschema:"when the adapter entered its state (RFC 3339)"`
		LastError string `json:"last_error,omitempty" jsonschema:"the error behind the latest backoff or stop"`
		Restarts  int    `json:"restarts" jsonschema:"how many times the adapter was restarted"`
		LastItem  string `json:"last_item,omitempty" jsonschema:"time of the newest stored item for this account (RFC 3339), a proxy for the last sync"`
	}
	// mcpPlanOut carries the plan and receipt as plain JSON values:
	// core.Receipt nests itself (fan-out recipients carry receipts), which
	// a JSON schema cannot describe.
	mcpPlanOut struct {
		Sent    bool `json:"sent"`
		Plan    any  `json:"plan" jsonschema:"what was (or would be) sent: action, channel, account, target, preview"`
		Receipt any  `json:"receipt,omitempty" jsonschema:"the delivery receipt, present only when sent"`
	}
)

func clampLimit(n int) int {
	if n <= 0 {
		return mcpListDefault
	}
	return min(n, mcpListMax)
}

// errSendNotAllowed is returned when a call asks to really send but the
// server was started without --allow-send.
var errSendNotAllowed = errors.New("nothing was sent: sending is disabled until the user starts bunker mcp with --allow-send")

func newMCPServer(dial mcpDialer, allowSend bool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "bunker", Title: "bunker inbox"}, &mcp.ServerOptions{Instructions: mcpInstructions})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := true
	outbound := &mcp.ToolAnnotations{DestructiveHint: &destructive}

	mcp.AddTool(server, &mcp.Tool{Name: "counts", Description: "Unread counts per channel and account.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpCountsOut, error) {
			counts, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (map[core.Channel]map[string]int, error) {
				return b.Counts(ctx)
			})
			return nil, mcpCountsOut{Unread: counts}, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "list", Description: "The newest items (conversations' latest messages and mails), with a short snippet of each body.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpListIn) (*mcp.CallToolResult, mcpItemsOut, error) {
			filter := core.Filter{Channel: core.Channel(in.Channel), Account: in.Account, Limit: clampLimit(in.Limit)}
			if in.Unread {
				unread := true
				filter.Unread = &unread
			}
			items, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Item, error) { return b.List(ctx, filter) })
			return nil, mcpItemsOut{Items: toMCPItems(items, mcpSnippetLimit)}, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "read", Description: "One item with its full body. Never marks it read.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadIn) (*mcp.CallToolResult, mcpItem, error) {
			item, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) (core.Item, error) { return b.Read(ctx, in.ID, false) })
			if err != nil {
				return nil, mcpItem{}, err
			}
			return nil, toMCPItem(item, mcpBodyLimit), nil
		})

	mcp.AddTool(server, &mcp.Tool{Name: "thread", Description: "The newest messages of one conversation, oldest first. Never marks anything read.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpThreadIn) (*mcp.CallToolResult, mcpItemsOut, error) {
			items, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Item, error) {
				return b.Thread(ctx, in.Channel, in.Account, in.Thread, time.Time{}, clampLimit(in.Limit))
			})
			return nil, mcpItemsOut{Items: toMCPItems(items, mcpBodyLimit/10)}, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "contacts", Description: "People, groups and rooms the user can write to, from address books and existing conversations.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpContactsIn) (*mcp.CallToolResult, mcpContactsOut, error) {
			filter := core.ContactFilter{Channel: core.Channel(in.Channel), Account: in.Account, Query: in.Query, Limit: clampLimit(in.Limit)}
			contacts, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Contact, error) { return b.Contacts(ctx, filter) })
			if contacts == nil {
				contacts = []core.Contact{}
			}
			return nil, mcpContactsOut{Contacts: contacts}, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "calls", Description: "Live voice calls.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpCallsOut, error) {
			calls, err := withBackend(ctx, dial, func(ctx context.Context, b Backend) ([]core.Call, error) { return b.Calls(ctx) })
			if calls == nil {
				calls = []core.Call{}
			}
			return nil, mcpCallsOut{Calls: calls}, err
		})

	mcp.AddTool(server, &mcp.Tool{Name: "health", Description: "Whether the bunker daemon is running and, per account, its connection state, last error and newest stored item time.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpNoInput) (*mcp.CallToolResult, mcpHealthOut, error) {
			return mcpHealth(ctx, dial)
		})

	mcp.AddTool(server, &mcp.Tool{Name: "send", Description: "Send a new message. Returns the plan only unless confirm is true and the server allows sending.", Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSendIn) (*mcp.CallToolResult, mcpPlanOut, error) {
			return mcpOutbound(ctx, dial, allowSend, in.Confirm, func(ctx context.Context, b Backend, dryRun bool) (core.Plan, core.Receipt, error) {
				channel := core.Channel(in.Channel)
				to, err := resolveRecipient(ctx, b, channel, in.Account, in.To, io.Discard, true)
				if err != nil {
					return core.Plan{}, core.Receipt{}, err
				}
				return b.Send(ctx, core.Outgoing{Channel: channel, Account: in.Account, To: []string{to}, Subject: in.Subject, Body: in.Text}, dryRun)
			})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "reply", Description: "Reply to an item. Returns the plan only unless confirm is true and the server allows sending.", Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpReplyIn) (*mcp.CallToolResult, mcpPlanOut, error) {
			return mcpOutbound(ctx, dial, allowSend, in.Confirm, func(ctx context.Context, b Backend, dryRun bool) (core.Plan, core.Receipt, error) {
				return b.Reply(ctx, in.ID, in.Text, nil, nil, dryRun)
			})
		})

	return server
}

// mcpOutbound always plans first. It only sends when the call confirms
// and the server allows it; a confirm on a plans-only server returns the
// plan together with the reason nothing was sent. A real send carries an
// idempotency key derived from the plan (issue #67), so an agent that
// retries a confirm after a timeout gets the first receipt back instead
// of sending the message twice.
func mcpOutbound(ctx context.Context, dial mcpDialer, allowSend, confirm bool, do func(context.Context, Backend, bool) (core.Plan, core.Receipt, error)) (*mcp.CallToolResult, mcpPlanOut, error) {
	type result struct {
		plan    core.Plan
		receipt *core.Receipt
	}
	timeout := mcpCallTimeout
	if confirm && allowSend {
		timeout = mcpSendTimeout
	}
	res, err := withBackendTimeout(ctx, dial, timeout, func(ctx context.Context, b Backend) (result, error) {
		plan, _, err := do(ctx, b, true)
		if err != nil || !confirm || !allowSend {
			return result{plan: plan}, err
		}
		ctx = core.WithIdempotencyKey(ctx, mcpIdempotencyKey(plan))
		plan, receipt, err := do(ctx, b, false)
		return result{plan: plan, receipt: &receipt}, err
	})
	if err != nil {
		return nil, mcpPlanOut{}, err
	}
	out := mcpPlanOut{Sent: res.receipt != nil, Plan: res.plan}
	if res.receipt != nil {
		out.Receipt = res.receipt
	}
	if confirm && !allowSend {
		plan, _ := json.Marshal(res.plan)
		text := errSendNotAllowed.Error() + ". This is what would be sent: " + string(plan)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	}
	return nil, out, nil
}

// mcpHealth answers the health tool. Unlike every other tool, a daemon
// that cannot be dialed is the answer, not a failure: the agent asked
// whether bunker is up, so "no, run bunker daemon" is a successful result.
func mcpHealth(ctx context.Context, dial mcpDialer) (*mcp.CallToolResult, mcpHealthOut, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	backend, closer, err := dial(ctx)
	if err != nil {
		return nil, mcpHealthOut{
			Error:    err.Error(),
			Hint:     "the bunker daemon is not running: start it with `bunker daemon` (or its service)",
			Adapters: []mcpAdapterHealth{},
		}, nil
	}
	if closer != nil {
		defer closer.Close()
	}
	adapters, err := backend.Health(ctx)
	if err != nil {
		return nil, mcpHealthOut{}, err
	}
	out := mcpHealthOut{DaemonUp: true, Adapters: make([]mcpAdapterHealth, 0, len(adapters))}
	for _, a := range adapters {
		h := mcpAdapterHealth{
			Channel: string(a.Channel), Account: a.Account, State: string(a.State),
			LastError: a.LastError, Restarts: a.Restarts,
		}
		if !a.Since.IsZero() {
			h.Since = a.Since.Format(time.RFC3339)
		}
		if t := newestItemTime(ctx, backend, a.Channel, a.Account); !t.IsZero() {
			h.LastItem = t.Format(time.RFC3339)
		}
		out.Adapters = append(out.Adapters, h)
	}
	return nil, out, nil
}

// newestItemTime is the timestamp of the newest stored item for
// channel/account: the daemon keeps no per-adapter sync clock, and the
// newest item is what tells an agent how fresh the data is. It is best
// effort, so a failing list only leaves the time out.
func newestItemTime(ctx context.Context, b Backend, channel core.Channel, account string) time.Time {
	items, err := b.List(ctx, core.Filter{Channel: channel, Account: account, Limit: 1})
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, it := range items {
		if it.Channel == channel && it.Account == account && it.Timestamp.After(newest) {
			newest = it.Timestamp
		}
	}
	return newest
}
