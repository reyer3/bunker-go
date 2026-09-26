package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestRelativeTimeFormatsTodayYesterdayAndOlder(t *testing.T) {
	now := time.Date(2026, 9, 26, 14, 30, 0, 0, time.UTC)
	tests := []struct {
		name string
		at   time.Time
		want string
	}{
		{"today morning", time.Date(2026, 9, 26, 10, 42, 0, 0, time.UTC), "10:42"},
		{"today same minute as now", now, "14:30"},
		{"yesterday", time.Date(2026, 9, 25, 23, 59, 0, 0, time.UTC), "ayer"},
		{"two days ago", time.Date(2026, 9, 24, 9, 58, 0, 0, time.UTC), "24-sep"},
		{"last december", time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), "01-dic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := relativeTime(tt.at, now); got != tt.want {
				t.Errorf("relativeTime(%v, %v) = %q, want %q", tt.at, now, got, tt.want)
			}
		})
	}
}

func TestLooksLikeRawIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"matrix room id", "!abcXYZ123:matrix.org", true},
		{"matrix room id no name", "!AbCdEfGh:example.org", true},
		{"bare numeric jid with server", "34600112233@s.whatsapp.net", true},
		{"bare numeric group jid", "120363012345678901@g.us", true},
		{"bare numeric no domain", "5215512345678", true},
		{"human name", "Bob Martín", false},
		{"human name with punctuation", "puerta \U0001F6AA ACME", false},
		{"empty", "", false},
		{"mail subject", "ANÁLISIS DE LOS COMPROMISOS", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeRawIdentifier(tt.in); got != tt.want {
				t.Errorf("looksLikeRawIdentifier(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRowTitlePrefersThreadNameThenSubjectThenSender(t *testing.T) {
	tests := []struct {
		name       string
		item       core.Item
		wantText   string
		wantDimmed bool
	}{
		{
			name:     "thread name wins",
			item:     core.Item{ThreadName: "Bob Martín", Subject: "ignored", From: core.Address{Name: "ignored"}},
			wantText: "Bob Martín",
		},
		{
			name:     "matrix room with no name falls back to sender",
			item:     core.Item{Thread: "!abcXYZ:matrix.org", From: core.Address{Name: "Alice Doe", ID: "@alice:matrix.org"}},
			wantText: "Alice Doe",
		},
		{
			name:     "matrix room name that is itself a raw id is skipped",
			item:     core.Item{ThreadName: "!abcXYZ:matrix.org", Subject: "", From: core.Address{Name: "Alice"}},
			wantText: "Alice",
		},
		{
			name:     "whatsapp bare numeric jid group name falls back to subject",
			item:     core.Item{ThreadName: "120363012345678901@g.us", Subject: "puerta \U0001F6AA ACME"},
			wantText: "puerta \U0001F6AA ACME",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, dimmed := rowTitle(tt.item)
			if text != tt.wantText || dimmed != tt.wantDimmed {
				t.Errorf("rowTitle(%+v) = (%q, %v), want (%q, %v)", tt.item, text, dimmed, tt.wantText, tt.wantDimmed)
			}
		})
	}
}

// TestRowTitleFallsBackToAShortenedDimmedIdentifier covers the case where
// nothing human-facing is available at all: the raw identifier must never
// reach the screen verbatim, only a short, explicitly dimmed stand-in.
func TestRowTitleFallsBackToAShortenedDimmedIdentifier(t *testing.T) {
	tests := []struct {
		name string
		item core.Item
	}{
		{"matrix room with no name and no participants", core.Item{Thread: "!abcXYZ0123456789:matrix.org"}},
		{"whatsapp with only a bare jid", core.Item{ID: "whatsapp:personal:1", From: core.Address{ID: "5215512345678@s.whatsapp.net"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, dimmed := rowTitle(tt.item)
			if !dimmed {
				t.Fatalf("rowTitle(%+v) dimmed = false, want true", tt.item)
			}
			if text == "" {
				t.Fatal("rowTitle returned an empty fallback")
			}
			for _, bad := range []string{"!", ":", "@"} {
				if strings.Contains(text, bad) {
					t.Errorf("shortened fallback %q still exposes protocol noise %q", text, bad)
				}
			}
			if w := runewidth.StringWidth(text); w > 13 {
				t.Errorf("shortened fallback %q is %d cells wide, want a short stand-in", text, w)
			}
		})
	}
}

func TestPreviewLineSanitizesAndTruncates(t *testing.T) {
	item := core.Item{
		From: core.Address{Name: "Carlos\x1b[31m"},
		Body: "abran porfa\x1b]0;title\x07 que llega el camión 🚚 con las piezas",
	}
	got := previewLine(item, 24)
	if got != "Carlos: abran porfa que…" {
		t.Errorf("previewLine = %q", got)
	}
	for _, bad := range []string{"\x1b", "\x07"} {
		if strings.Contains(got, bad) {
			t.Errorf("previewLine leaked an escape: %q", got)
		}
	}
}

func TestPreviewLineOmitsRawSender(t *testing.T) {
	item := core.Item{From: core.Address{ID: "34600112233@s.whatsapp.net"}, Body: "hola"}
	if got := previewLine(item, 40); got != "hola" {
		t.Errorf("previewLine = %q, want body only (no raw JID sender)", got)
	}
}

// TestRelativeTimeUsesNowsLocation covers the live bug: stored
// timestamps are UTC, so a 12:53 Lima message showed as "17:53".
func TestRelativeTimeUsesNowsLocation(t *testing.T) {
	lima := time.FixedZone("Lima", -5*3600)
	now := time.Date(2026, 9, 26, 13, 0, 0, 0, lima)
	at := time.Date(2026, 9, 26, 17, 53, 0, 0, time.UTC) // 12:53 in Lima
	if got := relativeTime(at, now); got != "12:53" {
		t.Errorf("relativeTime = %q, want 12:53 (now's location)", got)
	}
	lateUTC := time.Date(2026, 9, 26, 2, 0, 0, 0, time.UTC) // 21:00 on the 25th in Lima
	if got := relativeTime(lateUTC, now); got != "ayer" {
		t.Errorf("relativeTime = %q, want ayer (the day is decided in now's location)", got)
	}
}

// TestPreviewLineWithoutBodyShowsSenderAlone covers the live "Carol
// Ruiz:" rows: mail lists carry no body, so there is nothing after the
// colon.
func TestPreviewLineWithoutBodyShowsSenderAlone(t *testing.T) {
	item := core.Item{From: core.Address{Name: "Carol Ruiz"}}
	if got := previewLine(item, 40); got != "Carol Ruiz" {
		t.Errorf("previewLine = %q, want %q", got, "Carol Ruiz")
	}
}
