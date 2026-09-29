package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/reyer3/bunker-go/internal/core"
)

// The edit, delete and react tools (issues #76, #17) change a message
// the other side already has, so they are gated exactly like send:
// every call plans first (mcpOutbound), and only a call that sets
// confirm on a server started with --allow-send reaches the channel.
// A confirmed retry after a timeout carries the same plan-derived
// idempotency key, so it is never applied twice.

type (
	mcpEditIn struct {
		ID      string `json:"id" jsonschema:"the item id of our own message, as list or thread returns it"`
		Text    string `json:"text" jsonschema:"the new text"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true edits, and only works when the server runs with --allow-send"`
	}
	mcpDeleteIn struct {
		ID      string `json:"id" jsonschema:"the item id of our own message"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true deletes it for everyone, and only works when the server runs with --allow-send"`
	}
	mcpReactIn struct {
		ID      string `json:"id" jsonschema:"the item id of any message"`
		Emoji   string `json:"emoji,omitempty" jsonschema:"one emoji; empty removes our reaction"`
		Confirm bool   `json:"confirm,omitempty" jsonschema:"false (default) returns the plan only; true reacts, and only works when the server runs with --allow-send"`
	}
)

// addMCPMessageTools registers edit, delete and react on server.
func addMCPMessageTools(server *mcp.Server, dial mcpDialer, allowSend bool) {
	destructive := true
	outbound := &mcp.ToolAnnotations{DestructiveHint: &destructive}
	const gate = " Returns the plan only unless confirm is true and the server allows sending."

	mcp.AddTool(server, &mcp.Tool{Name: "edit", Description: "Edit the text of one of the user's own WhatsApp or Matrix messages (WhatsApp only within 20 minutes of sending). Everyone in the chat sees the change." + gate, Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpEditIn) (*mcp.CallToolResult, mcpPlanOut, error) {
			return mcpOutbound(ctx, dial, allowSend, in.Confirm, func(ctx context.Context, b Backend, dryRun bool) (core.Plan, core.Receipt, error) {
				return b.EditMessage(ctx, in.ID, in.Text, dryRun)
			})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "delete", Description: "Delete one of the user's own WhatsApp or Matrix messages for everyone. It cannot be undone." + gate, Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpDeleteIn) (*mcp.CallToolResult, mcpPlanOut, error) {
			return mcpOutbound(ctx, dial, allowSend, in.Confirm, func(ctx context.Context, b Backend, dryRun bool) (core.Plan, core.Receipt, error) {
				return b.DeleteMessage(ctx, in.ID, dryRun)
			})
		})

	mcp.AddTool(server, &mcp.Tool{Name: "react", Description: "React to a WhatsApp or Matrix message with an emoji, replacing the user's previous reaction on it; an empty emoji removes it. The sender sees it." + gate, Annotations: outbound},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpReactIn) (*mcp.CallToolResult, mcpPlanOut, error) {
			return mcpOutbound(ctx, dial, allowSend, in.Confirm, func(ctx context.Context, b Backend, dryRun bool) (core.Plan, core.Receipt, error) {
				return b.React(ctx, in.ID, in.Emoji, dryRun)
			})
		})
}
