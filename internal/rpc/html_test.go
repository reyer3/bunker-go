package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
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

// oldDaemon answers like a daemon built before --html: it ignores the html
// fields, so its plans never say html:true. It records every request.
func oldDaemon(t *testing.T) (socket string, calls func() []string) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), "old.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var seen []string
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			var req Request
			if json.Unmarshal(sc.Bytes(), &req) != nil {
				return
			}
			var p struct {
				DryRun bool `json:"dryRun"`
			}
			json.Unmarshal(req.Params, &p)
			mu.Lock()
			seen = append(seen, fmt.Sprintf("%s dryRun=%v", req.Method, p.DryRun))
			mu.Unlock()
			result, _ := json.Marshal(planReceiptResult{Plan: core.Plan{Action: req.Method}})
			line, _ := json.Marshal(Response{ID: req.ID, Result: result})
			conn.Write(append(line, '\n'))
		}
	}()
	return socket, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func TestClientRefusesHTMLOnDaemonWithoutSupport(t *testing.T) {
	socket, calls := oldDaemon(t)
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Body: "<p>x</p>", HTML: true}
	for _, dryRun := range []bool{true, false} {
		if _, _, err := client.Send(context.Background(), out, dryRun); !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("Send html dryRun=%v err = %v, want ErrUnsupported", dryRun, err)
		}
		if _, _, err := client.Reply(core.WithHTML(context.Background()), "mail:cl:1", "<p>x</p>", nil, nil, nil, dryRun); !errors.Is(err, core.ErrUnsupported) {
			t.Errorf("Reply html dryRun=%v err = %v, want ErrUnsupported", dryRun, err)
		}
	}
	for _, c := range calls() {
		if !strings.HasSuffix(c, "dryRun=true") {
			t.Errorf("daemon got %q: an HTML request reached a daemon that cannot honor it as a real send", c)
		}
	}

	// A plain send is untouched: one real call, no probe.
	before := len(calls())
	plain := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"}, Body: "x"}
	if _, _, err := client.Send(context.Background(), plain, false); err != nil {
		t.Fatalf("plain Send: %v", err)
	}
	if got := calls()[before:]; len(got) != 1 || got[0] != "send dryRun=false" {
		t.Errorf("plain Send made calls %v, want exactly one real send", got)
	}
}
