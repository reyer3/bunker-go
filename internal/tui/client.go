package tui

import (
	"context"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
)

// Client is the daemon capability set needed by the interactive UI. Keeping
// this boundary injectable lets model tests run without a socket or accounts.
type Client interface {
	List(context.Context, core.Filter) ([]core.Item, error)
	Counts(context.Context) (map[core.Channel]map[string]int, error)
	Read(context.Context, string, bool) (core.Item, error)
	Reply(context.Context, string, string, []string, []string, bool) (core.Plan, core.Receipt, error)
	Organize(context.Context, string, core.OrganizeOp, bool) (core.Plan, error)
	Close() error
}

var _ Client = (*rpc.Client)(nil)
