package core

import (
	"regexp"
	"strings"
	"testing"
)

// Codex found newShortID returning the constant "abc123": every board,
// task, comment and invite with duplicate ids hit primary-key conflicts
// after the first create, and every invite code was "bunker-invite-abc123".
// These tests pin real uniqueness, still hex and still the same length
// commitment the invite-code prefix expects, without relying on a
// counter that a restart would reset.
func TestNewShortIDGeneratesDistinctValues(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newShortID()
		if len(id) != 24 {
			t.Fatalf("newShortID() = %q, want 24 hex characters", id)
		}
		if !regexp.MustCompile("^[0-9a-f]+$").MatchString(id) {
			t.Fatalf("newShortID() = %q, want lowercase hex only", id)
		}
		if seen[id] {
			t.Fatalf("newShortID() repeated %q across 200 calls", id)
		}
		seen[id] = true
	}
}

func TestNewIDIsPrefixedWithTheUniqueTail(t *testing.T) {
	a, b := newID(), newID()
	if !strings.HasPrefix(a, "id-") || !strings.HasPrefix(b, "id-") {
		t.Fatalf("newID() = %q, want the id- prefix", a)
	}
	if a == b {
		t.Fatalf("newID() repeated %q", a)
	}
}
