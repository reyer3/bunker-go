package tui

import (
	"context"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// Client is the daemon capability set needed by the interactive UI. Keeping
// this boundary injectable lets model tests run without a socket or accounts.
type Client interface {
	List(context.Context, core.Filter) ([]core.Item, error)
	Counts(context.Context) (map[core.Channel]map[string]int, error)
	Read(context.Context, string, bool) (core.Item, error)
	Reply(context.Context, string, string, []string, []string, []string, bool) (core.Plan, core.Receipt, error)
	Organize(context.Context, string, core.OrganizeOp, bool) (core.Plan, error)
	// Send backs K6's mail thread editor (reply/reply-all/forward), which
	// needs an editable To/Cc/Subject unlike Reply's server-derived
	// recipient.
	Send(context.Context, core.Outgoing, bool) (core.Plan, core.Receipt, error)
	// Thread, Presence, PresenceKeepalive and Typing back the chat/mail
	// thread view (K5/K6, conversation-view.md's contract).
	Thread(ctx context.Context, channel string, account, thread string, before time.Time, limit int) ([]core.Item, error)
	// ReadThread marks every unread, non-FromMe item of one conversation
	// read (see core.Service.ReadThread): the chat view (K5) and mail
	// thread view (K6) call it on open instead of Read/Organize on a
	// single item, fixing the bug where opening a conversation marked
	// only its newest item read.
	ReadThread(ctx context.Context, channel string, account, thread string, receipt bool) (int, error)
	Presence(ctx context.Context, channel string, account, thread string) (core.Presence, error)
	PresenceKeepalive(ctx context.Context, channel string, account, thread string, focused bool) error
	Typing(ctx context.Context, channel string, account, thread string, composing bool) error
	// Download saves item id's attachment at index to destPath (K5/K6's
	// `d` key), matching rpc.Client.Download's exact signature: the
	// daemon writes the file itself, so no bytes travel back over this
	// call, only the result (or a clear error).
	Download(ctx context.Context, id string, index int, destPath string, opts core.DownloadOptions) (core.DownloadResult, error)
	Close() error
	// Shared boards (kanban): delegate to core.BoardService through the
	// daemon (rpc.Client already implements these).
	BoardCreate(ctx context.Context, name, ownerID string) (*core.Board, error)
	BoardInvite(ctx context.Context, boardID, createdBy string, ttl time.Duration, maxUses int) (*core.InviteCode, error)
	BoardJoin(ctx context.Context, code, memberID, name, publicKey string) (*core.Member, error)
	BoardList(ctx context.Context) ([]*core.Board, error)
	BoardTasks(ctx context.Context, boardID string) ([]*core.Task, error)
	BoardTaskCreate(ctx context.Context, boardID, columnID, title, description, assigneeID string, dueDate *time.Time, tags []string, createdBy string) (*core.Task, error)
	BoardTaskMove(ctx context.Context, taskID, columnID string) (*core.Task, error)
	BoardTaskDone(ctx context.Context, taskID string, version int) (*core.Task, error)
	BoardTaskComment(ctx context.Context, taskID, memberID, text string) (*core.Comment, error)
	BoardMembers(ctx context.Context, boardID string) ([]*core.Member, error)
}

var (
	_ Client              = (*rpc.Client)(nil)
	_ MessageClient       = (*rpc.Client)(nil)
	_ ConversationsClient = (*rpc.Client)(nil)
	_ CallClient          = (*rpc.Client)(nil)
	_ MeetingsClient      = (*rpc.Client)(nil)
	_ PendingClient       = (*rpc.Client)(nil)
)
