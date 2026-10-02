package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func meetingsBackend(t *testing.T) *fakeBackend {
	t.Helper()
	start := time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC)
	m := core.Meeting{UID: "u1", Summary: "Revisión  semanal", Start: start, End: start.Add(time.Hour), URL: "https://meet.google.com/abc-defg-hij"}
	meta, err := core.MeetingMeta(nil, m, start)
	if err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	b.items["mail:cl:1"] = core.Item{ID: "mail:cl:1", Meta: meta}
	b.items["mail:cl:2"] = core.Item{ID: "mail:cl:2"}
	b.meetings = []core.UpcomingMeeting{
		{Meeting: core.Meeting{UID: "nolink", Summary: "Sin enlace", Start: start, End: start.Add(time.Hour)}, ItemID: "mail:cl:3"},
		{Meeting: m, ItemID: "mail:cl:1", Channel: core.ChannelMail, Account: "cl"},
	}
	return b
}

func stubOpener(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	prev := openMeetingURL
	openMeetingURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openMeetingURL = prev })
	return &opened
}

func TestCmdMeetingsJSONAndDays(t *testing.T) {
	b := meetingsBackend(t)
	code, out, _ := runCallCmd(t, b, "meetings", "--days", "3", "--json")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var got struct {
		Meetings []core.UpcomingMeeting `json:"meetings"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q: %v", out, err)
	}
	if len(got.Meetings) != 2 || got.Meetings[1].ItemID != "mail:cl:1" || got.Meetings[1].URL == "" {
		t.Fatalf("meetings = %+v", got.Meetings)
	}
	if len(b.meetingsCalls) != 1 || b.meetingsCalls[0].Days != 3 {
		t.Fatalf("filter = %+v", b.meetingsCalls)
	}
	_, empty, _ := runCallCmd(t, newFakeBackend(), "meetings", "--json")
	if strings.TrimSpace(empty) != `{"meetings":[]}` {
		t.Errorf("empty JSON = %q", empty)
	}
}

func TestCmdMeetingsText(t *testing.T) {
	code, out, _ := runCallCmd(t, meetingsBackend(t), "meetings")
	if code != 0 || !strings.Contains(out, "Revisión semanal\tMeet\tmail:cl:1") || !strings.Contains(out, "Sin enlace\tsin enlace\tmail:cl:3") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCmdMeetingsJoinDryRunDoesNotOpen(t *testing.T) {
	opened := stubOpener(t)
	t.Setenv("BUNKER_OPEN_URL", "mybrowser --app")
	code, out, _ := runCallCmd(t, meetingsBackend(t), "meetings", "join", "mail:cl:1", "--dry-run")
	if code != 0 || len(*opened) != 0 {
		t.Fatalf("code=%d opened=%v", code, *opened)
	}
	if !strings.Contains(out, "https://meet.google.com/abc-defg-hij") || !strings.Contains(out, "mybrowser --app https://meet.google.com/abc-defg-hij") {
		t.Errorf("out = %q, want the URL and the command", out)
	}
	_, out, _ = runCallCmd(t, meetingsBackend(t), "meetings", "join", "mail:cl:1", "--dry-run", "--json")
	var got struct {
		DryRun  bool     `json:"dryRun"`
		URL     string   `json:"url"`
		Command []string `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.DryRun || got.URL == "" || len(got.Command) != 3 {
		t.Fatalf("json = %q, %v", out, err)
	}
}

func TestCmdMeetingsJoinOpensAndNext(t *testing.T) {
	opened := stubOpener(t)
	if code, _, _ := runCallCmd(t, meetingsBackend(t), "meetings", "join", "mail:cl:1"); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if code, _, _ := runCallCmd(t, meetingsBackend(t), "meetings", "join", "next"); code != 0 {
		t.Fatalf("next code = %d", code)
	}
	want := "https://meet.google.com/abc-defg-hij"
	if len(*opened) != 2 || (*opened)[0] != want || (*opened)[1] != want {
		t.Fatalf("opened = %v, want next to skip the meeting without a link", *opened)
	}
}

func TestCmdMeetingsJoinErrors(t *testing.T) {
	opened := stubOpener(t)
	b := meetingsBackend(t)
	if code, _, stderr := runCallCmd(t, b, "meetings", "join", "mail:cl:2"); code == 0 || !strings.Contains(stderr, "no meeting") {
		t.Errorf("item without meeting: code=%d stderr=%q", code, stderr)
	}
	b.items["mail:cl:4"] = core.Item{ID: "mail:cl:4", Meta: map[string]string{core.MetaMeeting: `{"uid":"x","summary":"Sala"}`}}
	if code, _, stderr := runCallCmd(t, b, "meetings", "join", "mail:cl:4"); code == 0 || !strings.Contains(stderr, "enlace") {
		t.Errorf("meeting without link: code=%d stderr=%q", code, stderr)
	}
	if code, _, _ := runCallCmd(t, b, "meetings", "join"); code != 2 {
		t.Errorf("missing id code = %d, want 2", code)
	}
	failing := meetingsBackend(t)
	failing.meetingsErr = errors.New("boom")
	if code, _, stderr := runCallCmd(t, failing, "meetings"); code == 0 || !strings.Contains(stderr, "boom") {
		t.Errorf("backend error: code=%d stderr=%q", code, stderr)
	}
	openMeetingURL = func(string) error { return errors.New("sin opener") }
	if code, _, stderr := runCallCmd(t, meetingsBackend(t), "meetings", "join", "mail:cl:1"); code == 0 || !strings.Contains(stderr, "sin opener") {
		t.Errorf("opener error: code=%d stderr=%q", code, stderr)
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v", *opened)
	}
}
