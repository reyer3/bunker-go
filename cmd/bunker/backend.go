package main

import (
	"context"

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
	Get(ctx context.Context, id string) (core.Item, error)
	Fetch(ctx context.Context, id string) (core.Item, error)
	Read(ctx context.Context, id string, markReceipt bool) (core.Item, error)
	Counts(ctx context.Context) (map[core.Channel]map[string]int, error)
	Reply(ctx context.Context, id, body string, cc, attachments []string, dryRun bool) (core.Plan, core.Receipt, error)
	Send(ctx context.Context, out core.Outgoing, dryRun bool) (core.Plan, core.Receipt, error)
	Organize(ctx context.Context, id string, op core.OrganizeOp, dryRun bool) (core.Plan, error)
	PostStatus(ctx context.Context, channel core.Channel, account string, status core.Status, dryRun bool) (core.Plan, core.Receipt, error)
}

var (
	_ Backend = (*core.Service)(nil)
	_ Backend = (*rpc.Client)(nil)
)
