package mail

import "testing"

func TestSeqTrackerLookupAndExpungeShiftsHigherSeqNumsDown(t *testing.T) {
	tr := newSeqTracker()
	tr.track(1, 10)
	tr.track(2, 20)
	tr.track(3, 30)

	uid, ok := tr.expunge(2)
	if !ok || uid != 20 {
		t.Fatalf("expunge(2) = (%d, %v), want (20, true)", uid, ok)
	}

	if uid, ok := tr.lookup(1); !ok || uid != 10 {
		t.Errorf("seq 1 = (%d, %v), want (10, true) — unaffected by an expunge above it", uid, ok)
	}
	// seq 3 (uid 30) must have shifted down to seq 2, the way the server
	// renumbers every later message after an EXPUNGE — so seq 2 now
	// resolves to uid 30, not the expunged uid 20, and seq 3 is gone.
	if uid, ok := tr.lookup(3); ok {
		t.Errorf("seq 3 still present as %d, want it shifted to seq 2", uid)
	}
	if uid, ok := tr.lookup(2); !ok || uid != 30 {
		t.Errorf("seq 2 after shift = (%d, %v), want (30, true)", uid, ok)
	}
}

func TestSeqTrackerExpungeUntrackedSeqReportsNotFound(t *testing.T) {
	tr := newSeqTracker()
	tr.track(1, 10)
	if _, ok := tr.expunge(99); ok {
		t.Error("expunge of an untracked seq reported found, want not found")
	}
	// the tracked entry must be unaffected by an expunge that didn't
	// touch it.
	if uid, ok := tr.lookup(1); !ok || uid != 10 {
		t.Errorf("seq 1 = (%d, %v), want (10, true)", uid, ok)
	}
}
