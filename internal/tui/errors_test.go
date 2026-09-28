package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestHumanError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("rpc: send request: write unix @->/run/x.sock: %w", syscall.EPIPE), "sin conexión con el daemon"},
		{errors.New("rpc: dial unix /run/user/1000/bunker-go.sock: connect: no such file or directory"), "systemctl --user start bunker"},
		{fmt.Errorf("rpc: call: %w", context.DeadlineExceeded), "tardó demasiado"},
		{fmt.Errorf("core: read: %w", core.ErrNotFound), "(ya no existe)"},
		{fmt.Errorf("core: send: whatsapp: send: %q is not on WhatsApp", "+51"), `send: send: "+51" is not on WhatsApp`},
	}
	for _, c := range cases {
		if got := humanError(c.err); !strings.Contains(got, c.want) {
			t.Errorf("humanError(%v) = %q, want it to contain %q", c.err, got, c.want)
		}
	}
	// A missing attachment is ENOENT too, but it is not the daemon.
	_, err := os.Stat("/no/such/file.png")
	if got := humanError(err); strings.Contains(got, "daemon") {
		t.Errorf("a missing file was reported as the daemon being down: %q", got)
	}
	if got := humanError(errors.New("core: whatsapp: x")); strings.Contains(got, "core:") || strings.Contains(got, "whatsapp:") {
		t.Errorf("package prefixes leaked: %q", got)
	}
}
