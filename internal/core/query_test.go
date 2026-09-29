package core_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// queryNow is the fixed anchor every relative date below resolves
// against, in a non-UTC zone so absolute dates prove they use its zone.
var queryNow = time.Date(2026, 3, 31, 15, 30, 0, 0, time.FixedZone("UTC-5", -5*3600))

func TestParseQuery(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, queryNow.Location()) }
	text := func(v string) core.QueryTerm { return core.QueryTerm{Field: core.QueryText, Value: v} }

	cases := []struct {
		in   string
		want []core.QueryTerm
	}{
		{"", nil},
		{"   ", nil},
		{"factura", []core.QueryTerm{text("factura")}},
		{"factura marzo", []core.QueryTerm{text("factura"), text("marzo")}},
		{`"factura de marzo"`, []core.QueryTerm{{Field: core.QueryText, Value: "factura de marzo", Phrase: true}}},
		{"-spam", []core.QueryTerm{{Field: core.QueryText, Value: "spam", Negate: true}}},
		{`-"no leer"`, []core.QueryTerm{{Field: core.QueryText, Value: "no leer", Phrase: true, Negate: true}}},
		{"a - b", []core.QueryTerm{text("a"), text("-"), text("b")}},
		{"trailing -", []core.QueryTerm{text("trailing"), text("-")}},
		{"10:30", []core.QueryTerm{text("10:30")}},
		{`"http://x.test"`, []core.QueryTerm{{Field: core.QueryText, Value: "http://x.test", Phrase: true}}},
		{"from:ana", []core.QueryTerm{{Field: core.QueryFrom, Value: "ana"}}},
		{"FROM:ana", []core.QueryTerm{{Field: core.QueryFrom, Value: "ana"}}},
		{`from:"Ana María"`, []core.QueryTerm{{Field: core.QueryFrom, Value: "Ana María", Phrase: true}}},
		{"-from:ana@example.com", []core.QueryTerm{{Field: core.QueryFrom, Value: "ana@example.com", Negate: true}}},
		{"to:equipo subject:informe", []core.QueryTerm{{Field: core.QueryTo, Value: "equipo"}, {Field: core.QuerySubject, Value: "informe"}}},
		{"is:unread", []core.QueryTerm{{Field: core.QueryIs, Value: "unread"}}},
		{"is:READ", []core.QueryTerm{{Field: core.QueryIs, Value: "read"}}},
		{"-is:unread", []core.QueryTerm{{Field: core.QueryIs, Value: "unread", Negate: true}}},
		{"has:attachment", []core.QueryTerm{{Field: core.QueryHas, Value: "attachment"}}},
		{"has:attachments", []core.QueryTerm{{Field: core.QueryHas, Value: "attachment"}}},
		{"in:inbox", []core.QueryTerm{{Field: core.QueryIn, Value: "inbox"}}},
		{`in:"Sent Items"`, []core.QueryTerm{{Field: core.QueryIn, Value: "Sent Items", Phrase: true}}},
		{"channel:WhatsApp", []core.QueryTerm{{Field: core.QueryChannel, Value: "whatsapp"}}},
		{"account:work label:vip", []core.QueryTerm{{Field: core.QueryAccount, Value: "work"}, {Field: core.QueryLabel, Value: "vip"}}},
		{"after:2026-01-15", []core.QueryTerm{{Field: core.QueryAfter, Value: "2026-01-15", Time: day(2026, 1, 15)}}},
		{"before:2026-02-01", []core.QueryTerm{{Field: core.QueryBefore, Value: "2026-02-01", Time: day(2026, 2, 1)}}},
		{"after:7d", []core.QueryTerm{{Field: core.QueryAfter, Value: "7d", Time: queryNow.AddDate(0, 0, -7)}}},
		{"after:2w", []core.QueryTerm{{Field: core.QueryAfter, Value: "2w", Time: queryNow.AddDate(0, 0, -14)}}},
		{"before:3m", []core.QueryTerm{{Field: core.QueryBefore, Value: "3m", Time: queryNow.AddDate(0, -3, 0)}}},
		{"-after:0d", []core.QueryTerm{{Field: core.QueryAfter, Value: "0d", Time: queryNow, Negate: true}}},
		{
			`from:ana -in:spam "orden de compra" has:attachment after:1m`,
			[]core.QueryTerm{
				{Field: core.QueryFrom, Value: "ana"},
				{Field: core.QueryIn, Value: "spam", Negate: true},
				{Field: core.QueryText, Value: "orden de compra", Phrase: true},
				{Field: core.QueryHas, Value: "attachment"},
				{Field: core.QueryAfter, Value: "1m", Time: queryNow.AddDate(0, -1, 0)},
			},
		},
	}
	for _, tc := range cases {
		got, err := core.ParseQueryAt(tc.in, queryNow)
		if err != nil {
			t.Errorf("ParseQueryAt(%q): %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got.Terms, tc.want) {
			t.Errorf("ParseQueryAt(%q) =\n  %+v\nwant\n  %+v", tc.in, got.Terms, tc.want)
		}
	}
}

func TestParseQueryErrors(t *testing.T) {
	cases := []struct {
		in, wantInErr string
	}{
		{"foo:bar", `"foo:"`},
		{"factura form:ana", `"form:"`},
		{"http://x.test", `"http:"`},
		{"-foo:bar", `"foo:"`},
		{"from:", "from: needs a value"},
		{"from: ana", "from: needs a value"},
		{`from:""`, "from: needs a value"},
		{"is:starred", "is:starred"},
		{"has:link", "has:link"},
		{"channel:telegram", "channel:telegram"},
		{"before:2026-13-01", "before:2026-13-01"},
		{"after:yesterday", "after:yesterday"},
		{"after:7y", "after:7y"},
		{"after:-7d", "after:-7d"},
		{"after:d", "after:d"},
		{`"sin cerrar`, "unterminated quote"},
		{`subject:"sin cerrar`, "unterminated quote"},
	}
	for _, tc := range cases {
		_, err := core.ParseQueryAt(tc.in, queryNow)
		if err == nil {
			t.Errorf("ParseQueryAt(%q): want an error", tc.in)
			continue
		}
		if !errors.Is(err, core.ErrInvalidQuery) {
			t.Errorf("ParseQueryAt(%q) error %v does not wrap ErrInvalidQuery", tc.in, err)
		}
		if !strings.Contains(err.Error(), tc.wantInErr) {
			t.Errorf("ParseQueryAt(%q) error %q should mention %q", tc.in, err, tc.wantInErr)
		}
		if !strings.HasPrefix(err.Error(), "core: ") {
			t.Errorf("ParseQueryAt(%q) error %q lacks the package prefix", tc.in, err)
		}
	}
}

func TestParseQueryUsesTheCurrentClock(t *testing.T) {
	before := time.Now()
	q, err := core.ParseQuery("after:1d")
	if err != nil {
		t.Fatal(err)
	}
	got := q.Terms[0].Time
	if got.Before(before.AddDate(0, 0, -1)) || got.After(time.Now().AddDate(0, 0, -1)) {
		t.Fatalf("after:1d = %v, want about a day before now", got)
	}
}

func TestCursorRoundTripAndRejectsGarbage(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	c := core.EncodeCursor(ts, "mail:cl:1")
	gotTS, gotID, err := core.DecodeCursor(c)
	if err != nil || !gotTS.Equal(ts) || gotID != "mail:cl:1" {
		t.Fatalf("DecodeCursor(EncodeCursor) = %v, %q, %v", gotTS, gotID, err)
	}
	for _, bad := range []string{"%%%", "bm90IGpzb24", "e30"} { // not base64, "not json", "{}"
		if _, _, err := core.DecodeCursor(bad); err == nil {
			t.Errorf("DecodeCursor(%q): want an error", bad)
		}
	}
}
