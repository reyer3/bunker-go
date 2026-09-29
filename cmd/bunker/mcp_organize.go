package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
)

// The organize tools (issue #66) change an item's read state, folder or
// labels. They look harmless but are not always private: marking a
// WhatsApp or Matrix message read sends the other side a read receipt.
// So they follow the send rules exactly: every call builds a plan first
// (what changes, on which channel, and whether anyone is notified) and
// only applies it when the server runs with --allow-send and the call
// sets confirm.

// mcpArchiveFolder is the folder archive moves to. The mail adapter
// resolves it to the server's archive mailbox (\Archive, an Archive or
// Archives folder, or Gmail's All Mail), the same as
// `bunker organize --move Archive`.
const mcpArchiveFolder = "Archive"

// errChangeNotAllowed is errSendNotAllowed's sibling for the organize
// tools: nothing is sent, but the change is just as gated.
var errChangeNotAllowed = errors.New("nothing was changed: changes are disabled until the user starts bunker mcp with --allow-send")

type (
	mcpOrganizeIDIn struct {
		ID      string `json:"id" jsonschema:"the item id, as list returns it"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true applies it, and only works when the server runs with --allow-send"`
	}
	mcpMoveIn struct {
		ID      string `json:"id" jsonschema:"the item id, as list returns it"`
		Folder  string `json:"folder" jsonschema:"the destination folder, e.g. Archive, Junk or a folder name"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true applies it, and only works when the server runs with --allow-send"`
	}
	mcpLabelIn struct {
		ID      string   `json:"id" jsonschema:"the item id, as list returns it"`
		Add     []string `json:"add,omitempty" jsonschema:"labels to add"`
		Remove  []string `json:"remove,omitempty" jsonschema:"labels to remove"`
		Confirm bool     `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true applies it, and only works when the server runs with --allow-send"`
	}
	// mcpOrganizePlan is what an organize tool would do (or did).
	mcpOrganizePlan struct {
		Action         string `json:"action" jsonschema:"mark_read, mark_unread, archive, move or label"`
		ID             string `json:"id"`
		Channel        string `json:"channel"`
		Account        string `json:"account"`
		Change         string `json:"change" jsonschema:"what changes, in words"`
		NotifiesSender bool   `json:"notifies_sender" jsonschema:"whether the other side is told (a read receipt)"`
		LocalOnly      bool   `json:"local_only,omitempty" jsonschema:"only bunker's own store changes; the channel cannot"`
	}
	mcpOrganizeOut struct {
		Done bool            `json:"done" jsonschema:"whether the change was applied"`
		Plan mcpOrganizePlan `json:"plan"`
	}
)

// addMCPOrganizeTools registers the organize tools on server. It lives
// apart from newMCPServer so these tools stay one self-contained unit.
func addMCPOrganizeTools(server *mcp.Server, dial mcpDialer, allowSend bool) {
	destructive := true
	gated := &mcp.ToolAnnotations{DestructiveHint: &destructive}
	const gate = " Returns the plan only unless confirm is true and the server allows changes."

	mcp.AddTool(server, &mcp.Tool{Name: "mark_read", Description: "Mark an item read. On WhatsApp and Matrix this sends the sender a read receipt." + gate, Annotations: gated},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpOrganizeIDIn) (*mcp.CallToolResult, mcpOrganizeOut, error) {
			seen := true
			return mcpOrganizeOp(ctx, dial, allowSend, in.Confirm, "mark_read", in.ID, core.OrganizeOp{Seen: &seen})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "mark_unread", Description: "Put an item back in the unread inbox. Nobody is notified; on WhatsApp and Matrix only bunker changes." + gate, Annotations: gated},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpOrganizeIDIn) (*mcp.CallToolResult, mcpOrganizeOut, error) {
			return mcpGatedChange(ctx, dial, allowSend, in.Confirm,
				func(ctx context.Context, b Backend) (mcpOrganizePlan, error) {
					return planOrganize(ctx, b, "mark_unread", in.ID, core.OrganizeOp{}, false)
				},
				func(ctx context.Context, b Backend, plan *mcpOrganizePlan) error {
					local, err := b.MarkUnread(ctx, in.ID)
					// Keep the plan's local-only when the daemon claims
					// otherwise: a chat channel never marks unread.
					plan.LocalOnly = plan.LocalOnly || local
					return err
				})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "archive", Description: "Move a mail to the archive. Mail only." + gate, Annotations: gated},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpOrganizeIDIn) (*mcp.CallToolResult, mcpOrganizeOut, error) {
			return mcpOrganizeOp(ctx, dial, allowSend, in.Confirm, "archive", in.ID, core.OrganizeOp{MoveTo: mcpArchiveFolder})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "move", Description: "Move a mail to another folder. Mail only." + gate, Annotations: gated},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpMoveIn) (*mcp.CallToolResult, mcpOrganizeOut, error) {
			if in.Folder == "" {
				return nil, mcpOrganizeOut{}, errors.New("mcp: move: folder is required")
			}
			return mcpOrganizeOp(ctx, dial, allowSend, in.Confirm, "move", in.ID, core.OrganizeOp{MoveTo: in.Folder})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "label", Description: "Add or remove labels on a mail (IMAP keywords, or Gmail labels). Mail only." + gate, Annotations: gated},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpLabelIn) (*mcp.CallToolResult, mcpOrganizeOut, error) {
			if len(in.Add) == 0 && len(in.Remove) == 0 {
				return nil, mcpOrganizeOut{}, errors.New("mcp: label: give at least one label to add or remove")
			}
			return mcpOrganizeOp(ctx, dial, allowSend, in.Confirm, "label", in.ID, core.OrganizeOp{AddLabels: in.Add, RemoveLabels: in.Remove})
		})
}

// mcpOrganizeOp plans and, when allowed, applies op through the same
// Organize RPC `bunker organize` uses. The plan also runs Organize as a
// dry run, so an account without organize support fails before confirm.
func mcpOrganizeOp(ctx context.Context, dial mcpDialer, allowSend, confirm bool, action, id string, op core.OrganizeOp) (*mcp.CallToolResult, mcpOrganizeOut, error) {
	return mcpGatedChange(ctx, dial, allowSend, confirm,
		func(ctx context.Context, b Backend) (mcpOrganizePlan, error) {
			return planOrganize(ctx, b, action, id, op, true)
		},
		func(ctx context.Context, b Backend, _ *mcpOrganizePlan) error {
			_, err := b.Organize(ctx, id, op, false)
			return err
		})
}

// planOrganize describes action on item id. Channels that cannot do it
// at all (folders and labels outside mail) fail here, loudly, instead of
// returning a plan that could never be applied. checkOrganize also dry
// runs op through the Organize RPC, so a missing adapter or one without
// organize support fails at plan time too.
func planOrganize(ctx context.Context, b Backend, action, id string, op core.OrganizeOp, checkOrganize bool) (mcpOrganizePlan, error) {
	if id == "" {
		return mcpOrganizePlan{}, fmt.Errorf("mcp: %s: id is required", action)
	}
	item, err := b.Get(ctx, id)
	if err != nil {
		return mcpOrganizePlan{}, err
	}
	if item.ID == "" {
		return mcpOrganizePlan{}, fmt.Errorf("mcp: %s: item %q: %w", action, id, core.ErrNotFound)
	}
	plan := mcpOrganizePlan{Action: action, ID: id, Channel: string(item.Channel), Account: item.Account}
	chat := item.Channel == core.ChannelWhatsApp || item.Channel == core.ChannelMatrix
	switch action {
	case "mark_read":
		plan.Change = "mark read"
		if chat {
			plan.Change += " and send a read receipt"
			plan.NotifiesSender = true
		} else {
			plan.Change += " on the mail server (\\Seen); the sender is not told"
		}
	case "mark_unread":
		plan.Change = "mark unread"
		if chat {
			plan.Change += " in bunker only: " + string(item.Channel) + " cannot mark a message unread"
			plan.LocalOnly = true
		} else {
			plan.Change += " on the mail server (clears \\Seen)"
		}
	case "archive":
		plan.Change = "move to the archive"
	case "move":
		plan.Change = fmt.Sprintf("move to folder %q", op.MoveTo)
	case "label":
		plan.Change = fmt.Sprintf("add labels %v, remove labels %v", op.AddLabels, op.RemoveLabels)
	}
	if chat && (op.MoveTo != "" || len(op.AddLabels) > 0 || len(op.RemoveLabels) > 0) {
		return mcpOrganizePlan{}, fmt.Errorf("mcp: %s: %s has no folders or labels: %w", action, item.Channel, core.ErrUnsupported)
	}
	if checkOrganize {
		if _, err := b.Organize(ctx, id, op, true); err != nil {
			return mcpOrganizePlan{}, err
		}
	}
	return plan, nil
}

// mcpGatedChange is mcpOutbound's sibling for changes that return no
// receipt: it always plans first, and applies only when the call
// confirms and the server allows it. A confirm on a plans-only server
// returns the plan together with the reason nothing changed.
func mcpGatedChange(ctx context.Context, dial mcpDialer, allowSend, confirm bool, plan func(context.Context, Backend) (mcpOrganizePlan, error), apply func(context.Context, Backend, *mcpOrganizePlan) error) (*mcp.CallToolResult, mcpOrganizeOut, error) {
	timeout := mcpCallTimeout
	if confirm && allowSend {
		// A read receipt goes through the same paced client as a send.
		timeout = mcpSendTimeout
	}
	out, err := withBackendTimeout(ctx, dial, timeout, func(ctx context.Context, b Backend) (mcpOrganizeOut, error) {
		p, err := plan(ctx, b)
		if err != nil || !confirm || !allowSend {
			return mcpOrganizeOut{Plan: p}, err
		}
		if err := apply(ctx, b, &p); err != nil {
			return mcpOrganizeOut{}, err
		}
		return mcpOrganizeOut{Done: true, Plan: p}, nil
	})
	if err != nil {
		return nil, mcpOrganizeOut{}, err
	}
	if confirm && !allowSend {
		raw, _ := json.Marshal(out.Plan)
		text := errChangeNotAllowed.Error() + ". This is what would change: " + string(raw)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
	}
	return nil, out, nil
}
