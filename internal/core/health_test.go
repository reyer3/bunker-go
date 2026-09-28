package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestHealthTrackerSnapshotIsEmptyWhenNothingTracked(t *testing.T) {
	tr := core.NewHealthTracker()
	if got := tr.Snapshot(); len(got) != 0 {
		t.Fatalf("Snapshot() = %+v, want empty", got)
	}
}

// TestHealthTrackerTracksLifecycleAndRestarts proves the full R4
// transition set: connecting -> connected (clears LastError) ->
// backoff (bumps Restarts, records LastError) -> stopped, in order,
// sorted by (channel, account) in Snapshot.
func TestHealthTrackerTracksLifecycleAndRestarts(t *testing.T) {
	tr := core.NewHealthTracker()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tr.SetConnecting(core.ChannelWhatsApp, "personal", t0)
	got := tr.Snapshot()
	if len(got) != 1 || got[0].State != core.AdapterConnecting || got[0].Restarts != 0 {
		t.Fatalf("after SetConnecting: %+v, want one connecting entry with Restarts=0", got)
	}
	if !got[0].Since.Equal(t0) {
		t.Fatalf("Since = %v, want %v", got[0].Since, t0)
	}

	t1 := t0.Add(time.Minute)
	tr.SetConnected(core.ChannelWhatsApp, "personal", t1)
	got = tr.Snapshot()
	if got[0].State != core.AdapterConnected || !got[0].Since.Equal(t1) {
		t.Fatalf("after SetConnected: %+v", got)
	}

	t2 := t1.Add(time.Minute)
	tr.SetBackoff(core.ChannelWhatsApp, "personal", t2, errors.New("dial refused"))
	got = tr.Snapshot()
	if got[0].State != core.AdapterBackoff || got[0].Restarts != 1 || got[0].LastError != "dial refused" {
		t.Fatalf("after SetBackoff: %+v, want backoff/Restarts=1/LastError=dial refused", got)
	}

	// A second connect/backoff cycle bumps Restarts again and clears
	// LastError while connected.
	t3 := t2.Add(time.Minute)
	tr.SetConnecting(core.ChannelWhatsApp, "personal", t3)
	tr.SetConnected(core.ChannelWhatsApp, "personal", t3.Add(time.Second))
	got = tr.Snapshot()
	if got[0].LastError != "" {
		t.Fatalf("LastError after reconnecting = %q, want cleared", got[0].LastError)
	}
	tr.SetBackoff(core.ChannelWhatsApp, "personal", t3.Add(2*time.Second), errors.New("boom again"))
	got = tr.Snapshot()
	if got[0].Restarts != 2 {
		t.Fatalf("Restarts = %d, want 2 after a second backoff", got[0].Restarts)
	}

	t4 := t3.Add(time.Hour)
	tr.SetStopped(core.ChannelWhatsApp, "personal", t4, nil)
	got = tr.Snapshot()
	if got[0].State != core.AdapterStopped || !got[0].Since.Equal(t4) {
		t.Fatalf("after SetStopped: %+v", got)
	}
	if got[0].Restarts != 2 {
		t.Fatalf("Restarts after SetStopped = %d, want unchanged 2", got[0].Restarts)
	}
}

// TestHealthTrackerSnapshotSortedByChannelThenAccount proves Snapshot's
// stable ordering across multiple adapters.
func TestHealthTrackerSnapshotSortedByChannelThenAccount(t *testing.T) {
	tr := core.NewHealthTracker()
	now := time.Now()
	tr.SetConnecting(core.ChannelWhatsApp, "b", now)
	tr.SetConnecting(core.ChannelWhatsApp, "a", now)
	tr.SetConnecting(core.ChannelMail, "z", now)

	got := tr.Snapshot()
	if len(got) != 3 {
		t.Fatalf("Snapshot() len = %d, want 3", len(got))
	}
	want := []struct {
		Channel core.Channel
		Account string
	}{
		{core.ChannelMail, "z"},
		{core.ChannelWhatsApp, "a"},
		{core.ChannelWhatsApp, "b"},
	}
	for i, w := range want {
		if got[i].Channel != w.Channel || got[i].Account != w.Account {
			t.Fatalf("Snapshot()[%d] = %+v, want channel=%s account=%s", i, got[i], w.Channel, w.Account)
		}
	}
}
