package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func meetingItem(t *testing.T, id string, at time.Time, m core.Meeting) core.Item {
	t.Helper()
	meta, err := core.MeetingMeta(map[string]string{"folder": "INBOX"}, m, at)
	if err != nil {
		t.Fatal(err)
	}
	return core.Item{ID: id, Channel: core.ChannelMail, Account: "cl", Thread: id, Subject: "Invitación", Timestamp: at, Meta: meta}
}

func TestMeetingsListsOnlyItemsWithAMeetingNotYetEnded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	upcoming := core.Meeting{UID: "u1", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour), URL: "https://zoom.us/j/1"}
	past := core.Meeting{UID: "u2", Start: now.Add(-30 * 24 * time.Hour), End: now.Add(-30*24*time.Hour + time.Hour)}
	for _, it := range []core.Item{
		meetingItem(t, "mail:cl:1", now, upcoming),
		meetingItem(t, "mail:cl:2", now, past),
		{ID: "mail:cl:3", Channel: core.ChannelMail, Account: "cl", Timestamp: now},
	} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Meetings(ctx, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "mail:cl:1" {
		t.Fatalf("Meetings = %v, want only mail:cl:1", got)
	}
	m, ok := core.MeetingFromItem(got[0])
	if !ok || m.UID != "u1" || m.URL != "https://zoom.us/j/1" || !m.Start.Equal(upcoming.Start) {
		t.Errorf("round trip = %+v %v", m, ok)
	}
	if got[0].Meta["folder"] != "INBOX" {
		t.Errorf("other meta lost: %v", got[0].Meta)
	}
}

func TestShallowMailUpsertKeepsTheMeeting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	full := meetingItem(t, "mail:cl:1", now, core.Meeting{UID: "u1", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)})
	if err := s.Upsert(ctx, full); err != nil {
		t.Fatal(err)
	}
	shallow := full
	shallow.Meta = map[string]string{"folder": "INBOX"}
	shallow.Unread = true
	if err := s.Upsert(ctx, shallow); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Meetings(ctx, now)
	if len(got) != 1 {
		t.Fatalf("Meetings = %v, want the meeting kept by a flags-only refresh", got)
	}
}

func TestBareLinkInRecentChatBodyBecomesALinkMeeting(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	recent := core.Item{ID: "whatsapp:p:1", Channel: core.ChannelWhatsApp, Account: "p", Thread: "g", ThreadName: "Equipo",
		Body: "únanse: https://meet.google.com/abc-defg-hij", Timestamp: now.Add(-time.Hour)}
	old := core.Item{ID: "whatsapp:p:2", Channel: core.ChannelWhatsApp, Account: "p", Thread: "g", ThreadName: "Equipo",
		Body: "https://zoom.us/j/55", Timestamp: now.Add(-48 * time.Hour)}
	plain := core.Item{ID: "whatsapp:p:3", Channel: core.ChannelWhatsApp, Account: "p", Thread: "g", Body: "hola", Timestamp: now}
	for _, it := range []core.Item{recent, old, plain} {
		if err := s.Upsert(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Meetings(ctx, now.Add(-time.Hour*24*14))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "whatsapp:p:1" {
		t.Fatalf("Meetings = %v, want only the recent link", got)
	}
	m, _ := core.MeetingFromItem(got[0])
	if !m.Link || m.URL != "https://meet.google.com/abc-defg-hij" || m.Summary != "Equipo" {
		t.Errorf("link meeting = %+v", m)
	}
}

func TestRevokedItemLeavesTheMeetingList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	it := core.Item{ID: "matrix:h:1", Channel: core.ChannelMatrix, Account: "h", Thread: "r", Body: "https://meet.jit.si/Sala", Timestamp: now}
	if err := s.Upsert(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeItem(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Meetings(ctx, now.Add(-time.Hour)); len(got) != 0 {
		t.Fatalf("Meetings = %v, want none after the revoke", got)
	}
}
