package mail

import (
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestParseItemID(t *testing.T) {
	account, uidValidity, uid, err := parseItemID("mail:cl:42.1234")
	if err != nil {
		t.Fatalf("parseItemID() error = %v", err)
	}
	if account != "cl" || uidValidity != 42 || uid != 1234 {
		t.Errorf("parseItemID() = (%q, %d, %d), want (cl, 42, 1234)", account, uidValidity, uid)
	}
}

func TestParseItemIDMalformed(t *testing.T) {
	for _, id := range []string{
		"whatsapp:cl:42.1234",
		"mail:cl:not-a-number",
		"mail:cl",
		"",
	} {
		if _, _, _, err := parseItemID(id); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("parseItemID(%q) error = %v, want core.ErrNotFound", id, err)
		}
	}
}
