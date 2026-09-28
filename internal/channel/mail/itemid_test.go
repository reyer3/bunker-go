package mail

import (
	"errors"
	"testing"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestParseItemID(t *testing.T) {
	account, folder, uidValidity, uid, err := parseItemID("mail:cl:42.1234")
	if err != nil {
		t.Fatalf("parseItemID() error = %v", err)
	}
	if account != "cl" || folder != "INBOX" || uidValidity != 42 || uid != 1234 {
		t.Errorf("parseItemID() = (%q, %q, %d, %d), want (cl, INBOX, 42, 1234)", account, folder, uidValidity, uid)
	}
}

// TestParseItemIDSent proves a Sent-folder id (K2) round-trips through
// itemID/parseItemID with folder="Sent", distinguishable from an INBOX id
// carrying the same account/uidvalidity/uid.
func TestParseItemIDSent(t *testing.T) {
	id := itemID("cl", "Sent", 42, 1234)
	account, folder, uidValidity, uid, err := parseItemID(id)
	if err != nil {
		t.Fatalf("parseItemID(%q) error = %v", id, err)
	}
	if account != "cl" || folder != "Sent" || uidValidity != 42 || uid != 1234 {
		t.Errorf("parseItemID(%q) = (%q, %q, %d, %d), want (cl, Sent, 42, 1234)", id, account, folder, uidValidity, uid)
	}
	if inbox := itemID("cl", "INBOX", 42, 1234); inbox == id {
		t.Errorf("itemID(Sent) = %q, must differ from the INBOX id %q for the same uidvalidity/uid", id, inbox)
	}
}

func TestParseItemIDMalformed(t *testing.T) {
	for _, id := range []string{
		"whatsapp:cl:42.1234",
		"mail:cl:not-a-number",
		"mail:cl",
		"",
	} {
		if _, _, _, _, err := parseItemID(id); !errors.Is(err, core.ErrNotFound) {
			t.Errorf("parseItemID(%q) error = %v, want core.ErrNotFound", id, err)
		}
	}
}
