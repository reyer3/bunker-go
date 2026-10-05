package main

import (
	"context"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// Backend is what the CLI needs to serve a command: either a live
// rpc.Client talking to the daemon, or (for render's read-only fallback)
// an in-process core.Service opened directly against the store. Both
// already satisfy this method set, so command code never cares which one
// it got.
type Backend interface {
	List(ctx context.Context, filter core.Filter) ([]core.Item, error)
	// ListPage is List with the query language and cursor pagination
	// (`bunker list --query`, `bunker find`, the MCP search tool).
	ListPage(ctx context.Context, filter core.Filter, query string) (core.Page, error)
	Get(ctx context.Context, id string) (core.Item, error)
	Fetch(ctx context.Context, id string) (core.Item, error)
	Read(ctx context.Context, id string, markReceipt bool) (core.Item, error)
	Counts(ctx context.Context) (map[core.Channel]map[string]int, error)
	Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error)
	Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error)
	Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error)
	PostStatus(ctx context.Context, channel core.Channel, account string, status core.Status, dryRun bool) (core.Plan, core.Receipt, error)
	Download(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error)
	Avatar(ctx context.Context, channel core.Channel, account, thread string) (core.AvatarResult, error)
	Thread(ctx context.Context, channel, account, thread string, before time.Time, limit int) ([]core.Item, error)
	// ReadThread marks every unread, non-FromMe item of one conversation
	// read, backing the K5/K6 read-on-open fix and `bunker read-thread`
	// (conversation-view.md's read-thread fix, K9).
	ReadThread(ctx context.Context, channel, account, thread string, receipt bool) (int, error)
	// HealthReport returns every adapter's current health snapshot (R4):
	// channel, account, connection state, since when, its last error (if
	// any) and how many times it has been restarted; plus the daemon's
	// cached update status (issue #105).
	HealthReport(ctx context.Context) (core.HealthReport, error)
	// Backfill runs a server-side history search since a point in time,
	// upserting whatever the store is missing (H2: mail-history,
	// `bunker backfill mail <account> --since ...`).
	Backfill(ctx context.Context, channel core.Channel, account, folder string, since time.Time, dryRun bool) (core.BackfillResult, error)
	// Search runs a server-side search and upserts every match (H3:
	// mail-history, `bunker search mail <account> ...`).
	Search(ctx context.Context, channel core.Channel, account string, criteria core.SearchCriteria) ([]core.Item, error)
	// PlaceCall, ControlCall and Calls drive voice calls (core.Caller):
	// `bunker call`, `bunker call answer|reject|hangup` and `bunker calls`.
	PlaceCall(ctx context.Context, channel core.Channel, account, to string, dryRun bool) (core.Plan, core.Call, error)
	ControlCall(ctx context.Context, id string, action core.CallAction, dryRun bool) (core.Plan, core.Call, error)
	Calls(ctx context.Context) ([]core.Call, error)
	// Contacts lists address-book entries and conversations matching a
	// filter: `bunker contacts`, and names given to send/call.
	Contacts(ctx context.Context, filter core.ContactFilter) ([]core.Contact, error)
	// Conversations lists conversations newest first with their unread
	// counts, read ones included (`bunker chats`).
	Conversations(ctx context.Context, filter core.ConversationFilter) ([]core.Conversation, error)
	// Meetings lists the upcoming meetings from invitations and recent
	// call links (`bunker meetings`).
	Meetings(ctx context.Context, filter core.MeetingFilter) ([]core.UpcomingMeeting, error)
	// AddTodo, Todos, CompleteTodo and ReopenTodo keep the local to-do
	// list (`bunker todo`): writes touch only bunker's store.
	AddTodo(ctx context.Context, todo core.Todo) (core.Todo, error)
	Todos(ctx context.Context, filter core.TodoFilter) ([]core.Todo, error)
	CompleteTodo(ctx context.Context, id string) (core.Todo, error)
	ReopenTodo(ctx context.Context, id string) (core.Todo, error)
	// MarkUnread puts an item back in the unread inbox (`bunker unread`);
	// localOnly reports that only bunker's store changed.
	MarkUnread(ctx context.Context, id string) (localOnly bool, err error)
	// EditMessage, DeleteMessage and React change a message already in a
	// conversation (`bunker edit`, `bunker delete`, `bunker react`):
	// edit and delete only our own, react anyone's (an empty emoji
	// removes our reaction).
	EditMessage(ctx context.Context, id, text string, dryRun bool) (core.Plan, core.Receipt, error)
	DeleteMessage(ctx context.Context, id string, dryRun bool) (core.Plan, core.Receipt, error)
	React(ctx context.Context, id, emoji string, dryRun bool) (core.Plan, core.Receipt, error)
}

var (
	_ Backend = (*core.Service)(nil)
	_ Backend = (*rpc.Client)(nil)
)
