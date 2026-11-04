package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

// The test daemon's mail adapter is a fake without core.HTMLSender, so an
// HTML request that reaches Service is refused with ErrUnsupported: the
// error proves the flag crossed the wire for both send and reply.
func TestClientForwardsHTMLFlag(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)

	_, _, err := client.Reply(core.WithHTML(context.Background()), "mail:cl:att", "<p>x</p>", nil, nil, nil, true)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("Reply with HTML err = %v, want ErrUnsupported", err)
	}
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Body: "<p>x</p>", HTML: true}
	if _, _, err := client.Send(context.Background(), out, true); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("Send with HTML err = %v, want ErrUnsupported", err)
	}
	if _, _, err := client.Reply(context.Background(), "mail:cl:att", "plain", nil, nil, nil, true); err != nil {
		t.Errorf("plain Reply dry-run err = %v, want none", err)
	}
}
