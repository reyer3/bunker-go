package rpc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/rpc"
	"github.com/reyer3/bunker-go/internal/store"
)

type fixedUpdates core.UpdateStatus

func (f fixedUpdates) Status() core.UpdateStatus { return core.UpdateStatus(f) }

func serveHealth(t *testing.T, updates core.UpdateSource) string {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := core.NewService(st, core.NewRegistry())
	tracker := core.NewHealthTracker()
	tracker.SetConnected(core.ChannelMail, "cl", time.Unix(1, 0))
	svc.SetHealthTracker(tracker)
	if updates != nil {
		svc.SetUpdateSource(updates)
	}
	socket := filepath.Join(dir, "bunker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rpc.NewServer(svc).Serve(ctx, socket); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	dialUntilReady(t, socket).Close()
	return socket
}

func TestHealthReportCarriesUpdateStatus(t *testing.T) {
	socket := serveHealth(t, fixedUpdates{Available: true, Latest: "0.13.0"})
	client, err := rpc.Dial(socket)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer client.Close()
	report, err := client.HealthReport(context.Background())
	if err != nil {
		t.Fatalf("HealthReport: %v", err)
	}
	if !report.Update.Available || report.Update.Latest != "0.13.0" || len(report.Adapters) != 1 {
		t.Fatalf("report = %+v", report)
	}
	// Health keeps its old shape for callers that only want adapters.
	adapters, err := client.Health(context.Background())
	if err != nil || len(adapters) != 1 {
		t.Fatalf("Health = %+v, %v", adapters, err)
	}
}

// TestHealthWireFields pins the JSON names every client (and older
// ones that only read adapters) sees on the socket.
func TestHealthWireFields(t *testing.T) {
	for _, tc := range []struct {
		name       string
		updates    core.UpdateSource
		wantAvail  bool
		wantLatest string
	}{
		{"update", fixedUpdates{Available: true, Latest: "0.13.0"}, true, "0.13.0"},
		{"no source", nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := serveHealth(t, tc.updates)
			conn, err := net.Dial("unix", socket)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte(`{"id":"1","method":"health"}` + "\n")); err != nil {
				t.Fatalf("write: %v", err)
			}
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var resp struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("decode %s: %v", line, err)
			}
			if _, ok := resp.Result["adapters"]; !ok {
				t.Fatalf("result lacks adapters: %s", line)
			}
			var avail bool
			if err := json.Unmarshal(resp.Result["update_available"], &avail); err != nil || avail != tc.wantAvail {
				t.Fatalf("update_available = %s, want %v", resp.Result["update_available"], tc.wantAvail)
			}
			var latest string
			if raw, ok := resp.Result["latest_version"]; ok {
				json.Unmarshal(raw, &latest)
			}
			if latest != tc.wantLatest {
				t.Fatalf("latest_version = %q, want %q", latest, tc.wantLatest)
			}
		})
	}
}
