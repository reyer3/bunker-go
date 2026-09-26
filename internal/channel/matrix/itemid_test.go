package matrix

import (
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestItemIDRoundTrip(t *testing.T) {
	got := itemID("work", id.RoomID("!room:example.org"), id.EventID("$event123"))
	const want = "matrix:work:!room:example.org/$event123"
	if got != want {
		t.Fatalf("itemID = %q, want %q", got, want)
	}

	account, roomID, eventID, err := parseItemID(got)
	if err != nil {
		t.Fatalf("parseItemID: %v", err)
	}
	if account != "work" {
		t.Errorf("account = %q, want work", account)
	}
	if roomID != "!room:example.org" {
		t.Errorf("roomID = %q, want !room:example.org", roomID)
	}
	if eventID != "$event123" {
		t.Errorf("eventID = %q, want $event123", eventID)
	}
}

func TestParseItemIDRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"mail:work:1",
		"matrix:work",
		"matrix:work:noeventseparator",
	}
	for _, c := range cases {
		if _, _, _, err := parseItemID(c); err == nil {
			t.Errorf("parseItemID(%q): expected an error, got nil", c)
		}
	}
}
