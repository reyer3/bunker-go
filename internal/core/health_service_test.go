package core_test

import (
	"context"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestServiceHealthWithoutTrackerReturnsEmpty proves a Service that never
// had SetHealthTracker called (most existing tests, and any Service
// wired only for its store/registry) reports no adapters rather than
// erroring.
func TestServiceHealthWithoutTrackerReturnsEmpty(t *testing.T) {
	store := newMemStore()
	svc := core.NewService(store, core.NewRegistry())

	got, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Health() = %+v, want empty", got)
	}
}

// TestServiceHealthReturnsTrackerSnapshot proves Service.Health forwards
// whatever the wired HealthTracker currently holds.
func TestServiceHealthReturnsTrackerSnapshot(t *testing.T) {
	store := newMemStore()
	svc := core.NewService(store, core.NewRegistry())
	tracker := core.NewHealthTracker()
	tracker.SetConnected(core.ChannelMail, "cl", time.Unix(1, 0))
	svc.SetHealthTracker(tracker)

	got, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
	if len(got) != 1 || got[0].Channel != core.ChannelMail || got[0].Account != "cl" || got[0].State != core.AdapterConnected {
		t.Fatalf("Health() = %+v, want one connected mail/cl entry", got)
	}
}
