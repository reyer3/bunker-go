package core

import (
	"context"
	"fmt"
)

type htmlCtx struct{}

// WithHTML returns ctx marking the next Reply's body as HTML. Like
// WithVoice it travels in the context so Reply keeps its signature across
// every Backend and fake; only the RPC client (which puts it on the wire)
// and Service (which honors it) look at it.
func WithHTML(ctx context.Context) context.Context {
	return context.WithValue(ctx, htmlCtx{}, true)
}

// IsHTML reports whether WithHTML marked ctx.
func IsHTML(ctx context.Context) bool {
	v, _ := ctx.Value(htmlCtx{}).(bool)
	return v
}

// checkHTML refuses an HTML body the adapter cannot deliver as HTML, and
// one that cannot be HTML at all (a voice note has no text): a channel
// that ignored the flag would send the markup as literal text.
func checkHTML(adapter Adapter, out Outgoing, action string) error {
	if !out.HTML {
		return nil
	}
	if _, ok := adapter.(HTMLSender); !ok {
		return fmt.Errorf("core: %s: adapter %s/%s cannot send HTML: %w", action, out.Channel, out.Account, ErrUnsupported)
	}
	if out.Voice {
		return fmt.Errorf("core: %s: a voice note has no HTML body: %w", action, ErrUnsupported)
	}
	return nil
}
