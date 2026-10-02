package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"
)

// chatsUsage is shown on a malformed chats command.
const chatsUsage = "usage: bunker chats [--channel c] [--account a] [--limit n] [--json]"

// cmdChats lists conversations the way a messaging app does: newest
// first, fully read ones included, each with its unread count. It is a
// read-only query; nothing reaches a channel.
func cmdChats(ctx context.Context, backend Backend, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("chats", stderr)
	channel := fs.String("channel", "", "only this channel (whatsapp, matrix, mail)")
	account := fs.String("account", "", "only this account")
	limit := fs.Int("limit", 0, "at most this many (default 50, at most 500)")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) > 0 || *limit < 0 {
		fmt.Fprintln(stderr, chatsUsage)
		return 2
	}
	convs, err := backend.Conversations(ctx, core.ConversationFilter{Channel: core.Channel(*channel), Account: *account, Limit: *limit})
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	if *jsonOut {
		if convs == nil {
			convs = []core.Conversation{}
		}
		writeJSON(stdout, map[string]any{"conversations": convs})
		return 0
	}
	if len(convs) == 0 {
		fmt.Fprintln(stdout, "no conversations found")
		return 0
	}
	for _, c := range convs {
		name := c.Last.ThreadName
		if name == "" {
			name = c.Last.From.Name
		}
		if name == "" {
			name = c.Last.Thread
		}
		// Fields are tab-separated and one record per line, so a name
		// with whitespace is flattened.
		fmt.Fprintf(stdout, "%s/%s\t%s\t%d unread\t%s\n", c.Last.Channel, c.Last.Account, strings.Join(strings.Fields(name), " "), c.Unread, c.Last.ID)
	}
	return 0
}
