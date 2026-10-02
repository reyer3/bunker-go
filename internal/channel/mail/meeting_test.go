package mail

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// inviteBody is a Google-style invitation: a plain text part and a
// text/calendar alternative, in a multipart/alternative body.
func inviteBody(method, uid string, start time.Time, extra ...string) string {
	cal := []string{
		"BEGIN:VCALENDAR", "METHOD:" + method, "BEGIN:VEVENT",
		"UID:" + uid,
		"SUMMARY:Revisión semanal",
		"DTSTART:" + start.UTC().Format("20060102T150405Z"),
		"DTEND:" + start.Add(time.Hour).UTC().Format("20060102T150405Z"),
		"ORGANIZER;CN=Ana:mailto:ana@example.com",
		"X-GOOGLE-CONFERENCE:https://meet.google.com/abc-defg-hij",
	}
	cal = append(cal, extra...)
	cal = append(cal, "END:VEVENT", "END:VCALENDAR")
	return "--bnd\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nInvitación a Revisión semanal\r\n" +
		"--bnd\r\nContent-Type: text/calendar; charset=utf-8; method=" + method + "\r\n\r\n" +
		strings.Join(cal, "\r\n") + "\r\n--bnd--\r\n"
}

// TestSyncStoresMeetingFromCalendarPart: a mail with a text/calendar
// part reaches the store with its meeting, listable as upcoming, and a
// later CANCEL for the same UID wins.
func TestSyncStoresMeetingFromCalendarPart(t *testing.T) {
	srv := newProfileIMAPServer(t, dovecotProfile(dovecotOptions{}))
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	srv.Seed(t, "INBOX", seedMessage{
		Subject:     "Invitación: Revisión semanal",
		ContentType: "multipart/alternative; boundary=bnd",
		Body:        inviteBody("REQUEST", "uid-1@example.com", start),
		Date:        time.Now().UTC().Add(-time.Hour),
	})
	srv.Seed(t, "INBOX", seedMessage{Subject: "Sin invitación", Body: "hola\r\n", Date: time.Now().UTC().Add(-time.Minute)})

	st, err := store.Open(filepath.Join(t.TempDir(), "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := srv.Config("cl")
	cfg.IndexBodyMaxKB = defaultIndexBodyMaxKB
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(srv.Addr))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, st) }()

	var items []core.Item
	pollUntil(5*time.Second, func() bool {
		items, err = st.Meetings(context.Background(), time.Now())
		return err == nil && len(items) > 0
	})
	if err != nil || len(items) != 1 {
		cancel()
		<-done
		t.Fatalf("Meetings = %v, %v; want the invitation only", items, err)
	}
	m, ok := core.MeetingFromItem(items[0])
	if !ok || m.UID != "uid-1@example.com" || m.Summary != "Revisión semanal" ||
		m.URL != "https://meet.google.com/abc-defg-hij" || !m.Start.Equal(start) || m.Organizer != "Ana <ana@example.com>" {
		t.Errorf("meeting = %+v %v", m, ok)
	}

	// A cancellation arrives as a new message; the service keeps only
	// the latest version and drops the cancelled meeting.
	srv.Seed(t, "INBOX", seedMessage{
		Subject:     "Cancelada: Revisión semanal",
		ContentType: "multipart/alternative; boundary=bnd",
		Body:        inviteBody("CANCEL", "uid-1@example.com", start, "SEQUENCE:1"),
		Date:        time.Now().UTC(),
	})
	var all []core.Item
	pollUntil(5*time.Second, func() bool {
		all, err = st.Meetings(context.Background(), time.Now())
		return err == nil && len(all) == 2
	})
	cancel()
	<-done
	if len(all) != 2 {
		t.Fatalf("Meetings = %d items, want request and cancel", len(all))
	}
	svc := core.NewService(st, core.NewRegistry())
	if got, err := svc.Meetings(context.Background(), core.MeetingFilter{}); err != nil || len(got) != 0 {
		t.Errorf("Service.Meetings = %v, %v; want none after the cancel", got, err)
	}
}

// TestSetMeetingFromICSAttachmentAndGarbage: an .ics attachment counts,
// and an unreadable calendar leaves the item without a meeting.
func TestSetMeetingFromICSAttachmentAndGarbage(t *testing.T) {
	raw := "From: a@example.com\r\nTo: b@example.com\r\nSubject: x\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=m\r\n\r\n" +
		"--m\r\nContent-Type: text/plain\r\n\r\ncuerpo\r\n" +
		"--m\r\nContent-Type: application/octet-stream; name=invite.ics\r\nContent-Disposition: attachment; filename=invite.ics\r\n\r\n" +
		"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:att-1\r\nDTSTART:20991005T100000Z\r\nLOCATION:https://us02web.zoom.us/j/123\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n" +
		"--m--\r\n"
	_, atts, cals, err := walkMessage([]byte(raw), false)
	if err != nil || len(cals) != 1 || len(atts) != 1 {
		t.Fatalf("walkMessage = %v %v %v", atts, cals, err)
	}
	item := core.Item{ID: "mail:cl:1", Timestamp: time.Now()}
	setMeeting(&item, cals)
	m, ok := core.MeetingFromItem(item)
	if !ok || m.URL != "https://us02web.zoom.us/j/123" {
		t.Errorf("meeting = %+v %v", m, ok)
	}

	bad := core.Item{ID: "mail:cl:2"}
	setMeeting(&bad, []string{"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART:zzz\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"})
	if _, ok := core.MeetingFromItem(bad); ok {
		t.Error("garbage calendar produced a meeting")
	}
}
