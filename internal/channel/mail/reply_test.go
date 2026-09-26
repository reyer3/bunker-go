package mail

import (
	"reflect"
	"testing"
	"time"
)

func TestReplySubject(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Meet recording", "Re: Meet recording"},
		{"Re: Meet recording", "Re: Meet recording"},
		{"RE: Meet recording", "RE: Meet recording"},
		{"", "Re:"},
	}
	for _, tt := range tests {
		if got := ReplySubject(tt.in); got != tt.want {
			t.Errorf("ReplySubject(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReplyReferences(t *testing.T) {
	tests := []struct {
		name           string
		origMessageID  string
		origReferences []string
		wantReferences []string
	}{
		{
			name:           "no prior references starts a new chain",
			origMessageID:  "<a@x>",
			origReferences: nil,
			wantReferences: []string{"<a@x>"},
		},
		{
			name:           "prior references are kept in order and extended",
			origMessageID:  "<b@x>",
			origReferences: []string{"<a@x>"},
			wantReferences: []string{"<a@x>", "<b@x>"},
		},
		{
			name:           "blank message id is not appended",
			origMessageID:  "",
			origReferences: []string{"<a@x>"},
			wantReferences: []string{"<a@x>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReplyReferences(tt.origMessageID, tt.origReferences)
			if !reflect.DeepEqual(got, tt.wantReferences) {
				t.Errorf("ReplyReferences(%q, %v) = %v, want %v", tt.origMessageID, tt.origReferences, got, tt.wantReferences)
			}
		})
	}
}

func TestQuoteBody(t *testing.T) {
	at := time.Date(2026, 9, 25, 10, 30, 0, 0, time.UTC)
	got := QuoteBody("line one\nline two", "Alice <alice@example.org>", at)
	want := "On 2026-09-25 10:30 UTC, Alice <alice@example.org> wrote:\n> line one\n> line two"
	if got != want {
		t.Errorf("QuoteBody() = %q, want %q", got, want)
	}
}
