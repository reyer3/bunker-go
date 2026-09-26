package mail

import (
	"reflect"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestExtractUID(t *testing.T) {
	tests := []struct {
		name string
		line string
		want imap.UID
		ok   bool
	}{
		{name: "simple", line: "* 3 FETCH (UID 1290 FLAGS (\\Seen))", want: 1290, ok: true},
		{name: "no UID token", line: "* 3 FETCH (FLAGS (\\Seen))", want: 0, ok: false},
		{name: "UID at line start of the parenthesized list", line: "* 1 FETCH (UID 42 X-GM-LABELS (\\Sent))", want: 42, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractUID(tt.line)
			if ok != tt.ok || got != tt.want {
				t.Errorf("extractUID(%q) = (%d, %v), want (%d, %v)", tt.line, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMatchParens(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "simple list", in: "(a b c)", want: "a b c", ok: true},
		{name: "quoted paren is not the close", in: `("a) b" c)`, want: `"a) b" c`, ok: true},
		{name: "nested parens balance", in: "(a (b c) d)", want: "a (b c) d", ok: true},
		{name: "escaped quote inside quoted string", in: `("a\"b" c)`, want: `"a\"b" c`, ok: true},
		{name: "no opening paren", in: "a b c", want: "", ok: false},
		{name: "unterminated", in: "(a b c", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := matchParens(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("matchParens(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseIMAPLabelList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "bare atoms", in: "bunker-test Work", want: []string{"bunker-test", "Work"}},
		{name: "quoted string with space", in: `"Muy Importante"`, want: []string{"Muy Importante"}},
		{name: "backslash system label keeps readable name", in: `\Sent`, want: []string{"Sent"}},
		{name: "backslash Inbox is dropped", in: `\Inbox bunker-test`, want: []string{"bunker-test"}},
		{name: "mixed atoms and quoted and system labels", in: `\Important "Muy Importante" bunker-test`, want: []string{"Important", "Muy Importante", "bunker-test"}},
		{name: "escaped quote and backslash inside quoted string", in: `"Quo\"te\\slash"`, want: []string{`Quo"te\slash`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIMAPLabelList(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseIMAPLabelList(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseFetchLabelsLine(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantUID    imap.UID
		wantLabels []string
		wantOK     bool
	}{
		{
			name:       "uid and labels",
			line:       `* 3 FETCH (UID 1290 X-GM-LABELS (\Important "Muy Importante" bunker-test))`,
			wantUID:    1290,
			wantLabels: []string{"Important", "Muy Importante", "bunker-test"},
			wantOK:     true,
		},
		{
			name:       "no X-GM-LABELS in this line",
			line:       `* 3 FETCH (UID 1290 FLAGS (\Seen))`,
			wantUID:    1290,
			wantLabels: nil,
			wantOK:     false,
		},
		{
			name:   "not a FETCH line",
			line:   `A1 OK Success`,
			wantOK: false,
		},
		{
			name:       "empty label list",
			line:       `* 1 FETCH (UID 7 X-GM-LABELS ())`,
			wantUID:    7,
			wantLabels: nil,
			wantOK:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, labels, ok := parseFetchLabelsLine(tt.line)
			if ok != tt.wantOK {
				t.Fatalf("parseFetchLabelsLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if uid != tt.wantUID || !reflect.DeepEqual(labels, tt.wantLabels) {
				t.Errorf("parseFetchLabelsLine(%q) = (%d, %#v), want (%d, %#v)", tt.line, uid, labels, tt.wantUID, tt.wantLabels)
			}
		})
	}
}
