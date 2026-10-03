package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// mcpSendTimeout bounds a confirmed send or reply, separately from
// mcpCallTimeout for reads: the daemon's human pacing (typing emulation,
// fan-out pauses) can take minutes, and a read-sized deadline would give
// up on a send that is still on its way. It is a variable only so tests
// can shorten it.
var mcpSendTimeout = 5 * time.Minute

// withBackendTimeout runs fn against a fresh daemon connection, bounded
// by timeout.
func withBackendTimeout[T any](ctx context.Context, dial mcpDialer, timeout time.Duration, fn func(context.Context, Backend) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	backend, closer, err := dial(ctx)
	if err != nil {
		return zero, err
	}
	if closer != nil {
		defer closer.Close()
	}
	return fn(ctx, backend)
}

// mcpIdempotencyKey derives a send's idempotency key from its confirmed
// plan (issue #67). An agent whose confirm timed out cannot tell whether
// the daemon went on to deliver, and will simply call again: deriving
// the key from what is being sent (channel, account, recipients or the
// replied-to item, subject, text, attachment paths and each attachment's
// name, MIME type and size) makes that retry find the first send instead
// of repeating it, with no state in the MCP server. The price is that the same text to the same person within
// core.IdempotencyTTL is answered with the first receipt (marked
// replayed) rather than sent again.
func mcpIdempotencyKey(plan core.Plan) string {
	h := sha256.New()
	writeKeyPart(h, plan.Action)
	writeKeyPart(h, string(plan.Channel))
	writeKeyPart(h, plan.Account)
	writeKeyPart(h, plan.Target)
	writeKeyParts(h, "to", plan.Recipients)
	writeKeyParts(h, "cc", plan.Cc)
	writeKeyPart(h, plan.Subject)
	writeKeyPart(h, plan.Preview)
	writeKeyParts(h, "attachments", plan.Media)
	// Name, MIME and size too: the same path with a different file behind
	// it (edited, or replaced) is a different send, not a retry.
	infos := make([]string, 0, len(plan.Attachments))
	for _, a := range plan.Attachments {
		infos = append(infos, fmt.Sprintf("%s|%s|%d|%t", a.Name, a.MIME, a.Size, a.Voice))
	}
	writeKeyParts(h, "attachment-info", infos)
	return "mcp-" + hex.EncodeToString(h.Sum(nil))
}

// writeKeyPart length-prefixes s so adjacent fields cannot run into each
// other ("ab"+"c" and "a"+"bc" hash differently).
func writeKeyPart(h hash.Hash, s string) {
	fmt.Fprintf(h, "%d:%s;", len(s), s)
}

func writeKeyParts(h hash.Hash, label string, parts []string) {
	writeKeyPart(h, fmt.Sprintf("%s[%d]", label, len(parts)))
	for _, p := range parts {
		writeKeyPart(h, p)
	}
}
