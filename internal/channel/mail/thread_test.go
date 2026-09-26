package mail

import "testing"

func TestThreadID(t *testing.T) {
	tests := []struct {
		name       string
		messageID  string
		inReplyTo  string
		references []string
		want       string
	}{
		{
			name:      "no references or in-reply-to uses its own message id",
			messageID: "<msg1@example.org>",
			want:      "<msg1@example.org>",
		},
		{
			name:      "in-reply-to only becomes the thread root",
			messageID: "<msg2@example.org>",
			inReplyTo: "<msg1@example.org>",
			want:      "<msg1@example.org>",
		},
		{
			name:       "references root wins over in-reply-to",
			messageID:  "<msg3@example.org>",
			inReplyTo:  "<msg2@example.org>",
			references: []string{"<msg1@example.org>", "<msg2@example.org>"},
			want:       "<msg1@example.org>",
		},
		{
			name:      "blank message id falls back to a stable placeholder",
			messageID: "",
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ThreadID(tt.messageID, tt.inReplyTo, tt.references)
			if got != tt.want {
				t.Errorf("ThreadID(%q, %q, %v) = %q, want %q", tt.messageID, tt.inReplyTo, tt.references, got, tt.want)
			}
		})
	}
}

func TestThreadName(t *testing.T) {
	tests := []struct {
		subject string
		want    string
	}{
		{"Re: Meet recording", "Meet recording"},
		{"RE: Re: Meet recording", "Meet recording"},
		{"Fwd: Incident 42", "Incident 42"},
		{"Fw: Incident 42", "Incident 42"},
		{"  Plain subject  ", "Plain subject"},
		{"", ""},
	}
	for _, tt := range tests {
		got := ThreadName(tt.subject)
		if got != tt.want {
			t.Errorf("ThreadName(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}
