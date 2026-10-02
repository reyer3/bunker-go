package core_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

type meetingStore struct {
	*memStore
	asked time.Time
}

func (s *meetingStore) Meetings(_ context.Context, endsAfter time.Time) ([]core.Item, error) {
	s.asked = endsAfter
	var out []core.Item
	for _, it := range s.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

var meetNow = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func meetItem(t *testing.T, id string, at time.Time, m core.Meeting) core.Item {
	t.Helper()
	meta, err := core.MeetingMeta(nil, m, at)
	if err != nil {
		t.Fatal(err)
	}
	return core.Item{ID: id, Channel: core.ChannelMail, Account: "cl", Timestamp: at, Meta: meta}
}

func timed(uid, summary string, start time.Time, d time.Duration) core.Meeting {
	return core.Meeting{UID: uid, Method: "REQUEST", Summary: summary, Start: start, End: start.Add(d), URL: "https://meet.google.com/abc-defg-hij"}
}

func meetingService(items ...core.Item) *core.Service {
	svc := core.NewService(&meetingStore{memStore: newMemStore(items...)}, core.NewRegistry())
	svc.SetQueryClock(func() time.Time { return meetNow })
	return svc
}

func TestMeetingsWindowOrderAndInProgress(t *testing.T) {
	h := time.Hour
	svc := meetingService(
		meetItem(t, "mail:cl:1", meetNow.Add(-48*h), timed("late", "Dentro de 3 días", meetNow.Add(72*h), h)),
		meetItem(t, "mail:cl:2", meetNow.Add(-48*h), timed("far", "En 10 días", meetNow.Add(240*h), h)),
		meetItem(t, "mail:cl:3", meetNow.Add(-48*h), timed("now", "En curso", meetNow.Add(-30*time.Minute), h)),
		meetItem(t, "mail:cl:4", meetNow.Add(-48*h), timed("ended", "Terminada", meetNow.Add(-3*h), h)),
		meetItem(t, "mail:cl:5", meetNow.Add(-48*h), timed("soon", "Pronto", meetNow.Add(25*time.Minute), h)),
	)
	got, err := svc.Meetings(context.Background(), core.MeetingFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var uids []string
	for _, m := range got {
		uids = append(uids, m.UID)
	}
	want := []string{"now", "soon", "late"}
	if len(uids) != len(want) {
		t.Fatalf("meetings = %v, want %v", uids, want)
	}
	for i := range want {
		if uids[i] != want[i] {
			t.Fatalf("meetings = %v, want %v", uids, want)
		}
	}
	if !got[0].InProgress(meetNow) || got[1].InProgress(meetNow) {
		t.Error("InProgress wrong")
	}
	wide, _ := svc.Meetings(context.Background(), core.MeetingFilter{Days: 30})
	if len(wide) != 4 {
		t.Errorf("30 days = %d meetings, want 4", len(wide))
	}
	limited, _ := svc.Meetings(context.Background(), core.MeetingFilter{Limit: 1})
	if len(limited) != 1 {
		t.Errorf("limit = %d", len(limited))
	}
}

func TestMeetingsDedupeKeepsLatestVersion(t *testing.T) {
	h := time.Hour
	v1 := timed("u", "Original", meetNow.Add(2*h), h)
	v2 := timed("u", "Movida", meetNow.Add(5*h), h)
	v2.Sequence = 1
	svc := meetingService(
		meetItem(t, "mail:cl:1", meetNow.Add(-5*h), v1),
		meetItem(t, "mail:cl:2", meetNow.Add(-3*h), v2),
	)
	got, _ := svc.Meetings(context.Background(), core.MeetingFilter{})
	if len(got) != 1 || got[0].Summary != "Movida" || got[0].ItemID != "mail:cl:2" {
		t.Fatalf("meetings = %+v, want only the moved version", got)
	}
	// Same sequence: the newer message wins even if its id sorts first.
	a := timed("same", "Vieja", meetNow.Add(2*h), h)
	b := timed("same", "Nueva", meetNow.Add(3*h), h)
	svc = meetingService(
		meetItem(t, "mail:cl:b", meetNow.Add(-1*h), b),
		meetItem(t, "mail:cl:a", meetNow.Add(-9*h), a),
	)
	got, _ = svc.Meetings(context.Background(), core.MeetingFilter{})
	if len(got) != 1 || got[0].Summary != "Nueva" {
		t.Fatalf("meetings = %+v, want the newer message", got)
	}
}

func TestMeetingsCancelledIsLeftOut(t *testing.T) {
	h := time.Hour
	req := timed("c", "A cancelar", meetNow.Add(2*h), h)
	cancel := req
	cancel.Method, cancel.Cancelled, cancel.Sequence = "CANCEL", true, 1
	svc := meetingService(
		meetItem(t, "mail:cl:1", meetNow.Add(-5*h), req),
		meetItem(t, "mail:cl:2", meetNow.Add(-1*h), cancel),
	)
	got, _ := svc.Meetings(context.Background(), core.MeetingFilter{})
	if len(got) != 0 {
		t.Fatalf("meetings = %+v, want none after the cancellation", got)
	}
}

func TestMeetingsLinksOnlyWhenRecentAndAfterTimed(t *testing.T) {
	h := time.Hour
	link := func(url string) core.Meeting {
		return core.Meeting{UID: "link:" + url, Method: core.MeetingMethodLink, Summary: "Enlace", URL: url, Link: true}
	}
	svc := meetingService(
		meetItem(t, "whatsapp:p:1", meetNow.Add(-2*h), link("https://zoom.us/j/1")),
		meetItem(t, "whatsapp:p:2", meetNow.Add(-30*h), link("https://zoom.us/j/2")),
		meetItem(t, "mail:cl:3", meetNow.Add(-48*h), timed("t", "Con hora", meetNow.Add(h), h)),
	)
	got, _ := svc.Meetings(context.Background(), core.MeetingFilter{})
	if len(got) != 2 || got[0].UID != "t" || !got[1].Link || got[1].URL != "https://zoom.us/j/1" {
		t.Fatalf("meetings = %+v, want timed first then the recent link only", got)
	}
}

func TestMeetingsRecurringNextOccurrence(t *testing.T) {
	h := time.Hour
	// Mondays 10:00 UTC from 2026-09-07; "now" is Monday 12:00, 2026-10-05.
	m := timed("weekly", "Semanal", time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC), h)
	m.Rule = "FREQ=WEEKLY;BYDAY=MO"
	svc := meetingService(meetItem(t, "mail:cl:1", meetNow.Add(-30*24*h), m))
	got, _ := svc.Meetings(context.Background(), core.MeetingFilter{})
	if len(got) != 1 || !got[0].Recurring || !got[0].Start.Equal(time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("meetings = %+v, want next Monday 10:00", got)
	}
	// An excluded occurrence is skipped; COUNT that has run out ends it.
	m.ExDates = []time.Time{time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)}
	svc = meetingService(meetItem(t, "mail:cl:1", meetNow.Add(-30*24*h), m))
	got, _ = svc.Meetings(context.Background(), core.MeetingFilter{Days: 10})
	if len(got) != 0 {
		t.Fatalf("meetings = %+v, want the excluded Monday skipped", got)
	}
	m.ExDates = nil
	m.Rule = "FREQ=WEEKLY;BYDAY=MO;COUNT=3"
	svc = meetingService(meetItem(t, "mail:cl:1", meetNow.Add(-30*24*h), m))
	got, _ = svc.Meetings(context.Background(), core.MeetingFilter{Days: 30})
	if len(got) != 0 {
		t.Fatalf("meetings = %+v, want none: COUNT=3 ended on 09-21", got)
	}
}

func TestMeetingMetaEndForRecurrence(t *testing.T) {
	m := timed("r", "x", time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC), time.Hour)
	m.Rule = "FREQ=DAILY;COUNT=3"
	meta, _ := core.MeetingMeta(nil, m, meetNow)
	if meta[core.MetaMeetingEnd] != "1788951600" { // 2026-09-09 11:00 UTC
		t.Errorf("meeting_end = %s", meta[core.MetaMeetingEnd])
	}
}

func TestMeetingsUnsupportedStore(t *testing.T) {
	svc := core.NewService(newMemStore(), core.NewRegistry())
	if _, err := svc.Meetings(context.Background(), core.MeetingFilter{}); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestMeetingProvider(t *testing.T) {
	for url, want := range map[string]string{
		"https://meet.google.com/abc-defg-hij":                    core.ProviderMeet,
		"https://us06web.zoom.us/j/123":                           core.ProviderZoom,
		"https://teams.microsoft.com/l/meetup-join/x/0":           core.ProviderTeams,
		"https://teams.live.com/meet/9300":                        core.ProviderTeams,
		"https://acme.webex.com/acme/j.php?MTID=1":                core.ProviderWebex,
		"https://meet.jit.si/Sala":                                core.ProviderJitsi,
		"https://meet.google.com/":                                "",
		"ftp://meet.google.com/abc-defg-hij":                      "",
		"https://evil.example/https://meet.google.com/abc-defg-h": "",
	} {
		if got := core.MeetingProvider(url); got != want {
			t.Errorf("MeetingProvider(%q) = %q, want %q", url, got, want)
		}
	}
}
