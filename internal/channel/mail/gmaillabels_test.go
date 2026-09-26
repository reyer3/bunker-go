package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestBuildXGMLabelsStoreLine(t *testing.T) {
	tests := []struct {
		name   string
		tag    string
		uid    uint32
		op     string
		labels []string
		want   string
	}{
		{
			name:   "add one label",
			tag:    "A1",
			uid:    42,
			op:     "+",
			labels: []string{"Important"},
			want:   "A1 UID STORE 42 +X-GM-LABELS (\"Important\")\r\n",
		},
		{
			name:   "remove multiple labels",
			tag:    "A2",
			uid:    7,
			op:     "-",
			labels: []string{"Work", "Follow up"},
			want:   "A2 UID STORE 7 -X-GM-LABELS (\"Work\" \"Follow up\")\r\n",
		},
		{
			name:   "replace with an empty set",
			tag:    "A3",
			uid:    1,
			op:     "",
			labels: nil,
			want:   "A3 UID STORE 1 X-GM-LABELS ()\r\n",
		},
		{
			name:   "quotes and backslashes are escaped",
			tag:    "A4",
			uid:    1,
			op:     "+",
			labels: []string{`Quo"te\slash`},
			want:   "A4 UID STORE 1 +X-GM-LABELS (\"Quo\\\"te\\\\slash\")\r\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildXGMLabelsStoreLine(tt.tag, imap.UID(tt.uid), tt.op, tt.labels)
			if got != tt.want {
				t.Errorf("buildXGMLabelsStoreLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckTagged(t *testing.T) {
	if err := checkTagged("A1", "A1 OK Success"); err != nil {
		t.Errorf("checkTagged(OK) error = %v, want nil", err)
	}
	if err := checkTagged("A1", "A1 NO [TRYCREATE] No such mailbox"); err == nil {
		t.Error("checkTagged(NO) error = nil, want an error")
	}
	if err := checkTagged("A1", "A1 BAD Syntax error"); err == nil {
		t.Error("checkTagged(BAD) error = nil, want an error")
	}
}
