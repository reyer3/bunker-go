package matrix

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSyncFilterExactJSON pins the exact wire shape of the sync filter.
// Synapse 1.68.0 rejects a filter whose presence part carries not_rooms
// with an HTTP 400 (the bug that broke gomuks against matrix.example.org).
// The filter built here must never emit "not_rooms" anywhere.
func TestSyncFilterExactJSON(t *testing.T) {
	got, err := json.Marshal(SyncFilter())
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}

	const want = `{"presence":{},"room":{"account_data":{},"ephemeral":{},"state":{"lazy_load_members":true},"timeline":{"limit":50}}}`
	if string(got) != want {
		t.Fatalf("filter JSON mismatch:\n got:  %s\n want: %s", got, want)
	}
}

func TestSyncFilterNeverUsesPresenceNotRooms(t *testing.T) {
	got, err := json.Marshal(SyncFilter())
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	if strings.Contains(string(got), "not_rooms") {
		t.Fatalf("filter JSON must never contain not_rooms (Synapse 1.68 rejects it with HTTP 400): %s", got)
	}
}
