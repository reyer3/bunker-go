// Package rpc exposes core.Service over a line-delimited JSON protocol on
// a unix socket, so the CLI (and Claude Code) can drive a running daemon
// without linking against core or store directly.
package rpc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// Request is one line-delimited JSON call.
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is one line-delimited JSON reply. ErrCode is set for the
// well-known sentinel errors (core.ErrNotFound, core.ErrUnsupported) so
// clients can recover them with errors.Is; Error always carries a
// human-readable message.
type Response struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
	ErrCode string          `json:"errCode,omitempty"`
}

const (
	errCodeNotFound    = "not_found"
	errCodeUnsupported = "unsupported"
)

// Method names understood by the server.
const (
	MethodList              = "list"
	MethodListPage          = "list_page"
	MethodGet               = "get"
	MethodFetch             = "fetch"
	MethodRead              = "read"
	MethodCounts            = "counts"
	MethodReply             = "reply"
	MethodSend              = "send"
	MethodOrganize          = "organize"
	MethodPostStatus        = "status"
	MethodDownload          = "download"
	MethodAvatar            = "avatar"
	MethodThread            = "thread"
	MethodReadThread        = "read_thread"
	MethodPresence          = "presence"
	MethodPresenceKeepalive = "presence_keepalive"
	MethodTyping            = "typing"
	MethodHealth            = "health"
	MethodBackfill          = "backfill"
	MethodSearch            = "search"
	MethodCall              = "call"
	MethodCallControl       = "call_control"
	MethodCalls             = "calls"
	MethodContacts          = "contacts"
	MethodConversations     = "conversations"
	MethodMeetings          = "meetings"
	MethodMarkUnread        = "mark_unread"
	MethodEdit              = "edit"
	MethodDelete            = "delete"
	MethodReact             = "react"
)

type listParams struct {
	Filter core.Filter `json:"filter"`
}
type listResult struct {
	Items []core.Item `json:"items"`
}

// listPageParams is MethodListPage's params: Filter's plain fields and
// Cursor, plus Query, the raw query-language string. The daemon parses
// Query (see core.Service.ListPage) so every client shares one grammar
// and one clock for relative dates. The result is a core.Page.
type listPageParams struct {
	Filter core.Filter `json:"filter"`
	Query  string      `json:"query,omitempty"`
}

type idParams struct {
	ID string `json:"id"`
}
type itemResult struct {
	Item core.Item `json:"item"`
}

// backfillParams is MethodBackfill's params (H2: mail-history).
type backfillParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Folder  string       `json:"folder"`
	Since   time.Time    `json:"since"`
	DryRun  bool         `json:"dryRun"`
}
type backfillResult struct {
	Result core.BackfillResult `json:"result"`
}

// searchParams is MethodSearch's params (H3: mail-history). Its result
// reuses listResult: Search prints exactly like list.
type searchParams struct {
	Channel  core.Channel        `json:"channel"`
	Account  string              `json:"account"`
	Criteria core.SearchCriteria `json:"criteria"`
}

// readParams is MethodRead's params: fetch item ID's full body and,
// only when MarkReceipt is true (the CLI's --mark-read), mark it read on
// the channel itself when the adapter supports it (T13c).
type readParams struct {
	ID          string `json:"id"`
	MarkReceipt bool   `json:"markReceipt"`
}

type countsResult struct {
	Counts map[core.Channel]map[string]int `json:"counts"`
}

// replyParams and sendParams carry an optional IdempotencyKey (issue
// #67): the daemon sends at most once per key and answers a repeat with
// the first call's receipt (see core.WithIdempotencyKey). Client reads it
// from the call's context and Server puts it back into one.
type replyParams struct {
	ID             string   `json:"id"`
	Body           string   `json:"body"`
	Cc             []string `json:"cc"`
	Attachments    []string `json:"attachments"`
	DryRun         bool     `json:"dryRun"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	// Voice sends the single attachment as a voice note (core.WithVoice).
	Voice bool `json:"voice,omitempty"`
}

type sendParams struct {
	Outgoing       core.Outgoing `json:"outgoing"`
	DryRun         bool          `json:"dryRun"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
}

// editParams, deleteParams and reactParams are MethodEdit's,
// MethodDelete's and MethodReact's params (issues #76, #17). They carry
// an optional IdempotencyKey like replyParams; the result is a
// planReceiptResult. An empty Emoji removes our reaction.
type editParams struct {
	ID             string `json:"id"`
	Text           string `json:"text"`
	DryRun         bool   `json:"dryRun"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type deleteParams struct {
	ID             string `json:"id"`
	DryRun         bool   `json:"dryRun"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type reactParams struct {
	ID             string `json:"id"`
	Emoji          string `json:"emoji"`
	DryRun         bool   `json:"dryRun"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type planReceiptResult struct {
	Plan    core.Plan    `json:"plan"`
	Receipt core.Receipt `json:"receipt"`
}

type organizeParams struct {
	ID     string          `json:"id"`
	Op     core.OrganizeOp `json:"op"`
	DryRun bool            `json:"dryRun"`
}

// callParams is MethodCall's params.
type callParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	To      string       `json:"to"`
	DryRun  bool         `json:"dryRun"`
}

// callControlParams is MethodCallControl's params.
type callControlParams struct {
	ID     string          `json:"id"`
	Action core.CallAction `json:"action"`
	DryRun bool            `json:"dryRun"`
}

type planCallResult struct {
	Plan core.Plan `json:"plan"`
	Call core.Call `json:"call"`
}

type callsResult struct {
	Calls []core.Call `json:"calls"`
}

type markUnreadParams struct {
	ID string `json:"id"`
}
type markUnreadResult struct {
	LocalOnly bool `json:"local_only"`
}

type contactsParams struct {
	Filter core.ContactFilter `json:"filter"`
}
type contactsResult struct {
	Contacts []core.Contact `json:"contacts"`
}

type conversationsParams struct {
	Filter core.ConversationFilter `json:"filter"`
}
type conversationsResult struct {
	Conversations []core.Conversation `json:"conversations"`
}

type meetingsParams struct {
	Filter core.MeetingFilter `json:"filter"`
}
type meetingsResult struct {
	Meetings []core.UpcomingMeeting `json:"meetings"`
}

type planResult struct {
	Plan core.Plan `json:"plan"`
}

type statusParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Status  core.Status  `json:"status"`
	DryRun  bool         `json:"dryRun"`
}

// downloadParams is MethodDownload's params: save item ID's attachment
// at Index to Path on the machine the daemon runs on. The daemon writes
// the file itself instead of returning its bytes (see Client.Download
// and core.Service.Download): the CLI and the daemon always run as the
// same user on the same machine, so this is both simpler and keeps every
// download under one enforced size cap, instead of also needing to fit
// a 100 MB attachment through this line-delimited JSON protocol's much
// smaller read buffer (see Server.handleConn's 8 MB scanner buffer).
type downloadParams struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Path  string `json:"path"`
	Force bool   `json:"force"`
}
type downloadResult struct {
	Result core.DownloadResult `json:"result"`
}

// avatarParams is MethodAvatar's params: the same (channel, account,
// thread) triple that groups Items into one conversation.
type avatarParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
}
type avatarResult struct {
	Result core.AvatarResult `json:"result"`
}

// threadParams is MethodThread's params: one conversation's items,
// oldest→newest, at most Limit items strictly before Before (zero =
// newest). Channel/Account/Thread together identify the conversation,
// the same triple avatarParams uses.
type threadParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
	Before  time.Time    `json:"before"`
	Limit   int          `json:"limit"`
}
type threadResult struct {
	Items []core.Item `json:"items"`
}

// readThreadParams is MethodReadThread's params: mark every unread,
// non-FromMe item of one conversation read (see core.Service.ReadThread).
// Unless Receipt is false (the CLI's --no-receipt), it also notifies the
// channel itself (WhatsApp/Matrix read receipts, mail \Seen).
type readThreadParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
	Receipt bool         `json:"receipt"`
}
type readThreadResult struct {
	Count int `json:"count"`
}

// presenceParams is MethodPresence's params: the same (channel, account,
// thread) triple avatarParams/threadParams use.
type presenceParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
}
type presenceResult struct {
	Presence core.Presence `json:"presence"`
}

// presenceKeepaliveParams is MethodPresenceKeepalive's params: the
// availability lease (see internal/core/presence.go) the TUI renews by
// calling this every <=20s while a chat view for Thread is open.
type presenceKeepaliveParams struct {
	Channel core.Channel `json:"channel"`
	Account string       `json:"account"`
	Thread  string       `json:"thread"`
	Focused bool         `json:"focused"`
}

// typingParams is MethodTyping's params: a thin, validated forward to
// the adapter's TypingSender. The TUI throttles the send rate itself
// (at most every 5s while typing; Composing=false on idle, send or
// leave); the daemon never re-derives that timing.
type typingParams struct {
	Channel   core.Channel `json:"channel"`
	Account   string       `json:"account"`
	Thread    string       `json:"thread"`
	Composing bool         `json:"composing"`
}

// healthResult is MethodHealth's result: every adapter's current
// core.AdapterHealth (R4), in the order core.Service.Health returns it.
//
// UpdateAvailable and LatestVersion (issue #105) were added later as
// top-level siblings of adapters: an older client decodes only adapters
// and ignores them, and an older daemon's answer decodes as "no update".
type healthResult struct {
	Adapters        []core.AdapterHealth `json:"adapters"`
	UpdateAvailable bool                 `json:"update_available"`
	LatestVersion   string               `json:"latest_version,omitempty"`
}

// DefaultSocketPath returns BUNKER_SOCKET if set, else
// $XDG_RUNTIME_DIR/bunker-go.sock, else a bunker-go.sock under os.TempDir().
func DefaultSocketPath() string {
	if p := os.Getenv("BUNKER_SOCKET"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "bunker-go.sock")
	}
	return filepath.Join(os.TempDir(), "bunker-go.sock")
}
