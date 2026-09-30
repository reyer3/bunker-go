package core_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestPlanAndReceiptJSONKeysAreSnakeCase pins the wire contract of issue
// #68: every key of a Plan and Receipt, nested ones included, is
// lower snake_case, so a consumer never sees the Go field names.
func TestPlanAndReceiptJSONKeysAreSnakeCase(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	plan := core.Plan{
		Action: "send", Channel: core.ChannelWhatsApp, Account: "wa", Target: "+1",
		Cc: []string{"cc@x"}, Subject: "s", Preview: "p", Media: []string{"/a.png"},
		Attachments:    []core.AttachmentInfo{{Name: "a.png", MIME: "image/png", Size: 3}},
		Recipients:     []string{"+1", "+2"},
		FanoutPauseMin: 3 * time.Second, FanoutPauseMax: 8 * time.Second,
	}
	receipt := core.Receipt{
		ID: "r1", Channel: core.ChannelWhatsApp, At: at, Replayed: true,
		Recipients: []core.RecipientResult{{To: "+1", Receipt: core.Receipt{ID: "r1", At: at}, Error: "boom"}},
	}

	wantPlan := []string{"account", "action", "attachments", "cc", "channel", "fanout_pause_max", "fanout_pause_min", "media", "preview", "recipients", "subject", "target"}
	wantReceipt := []string{"at", "channel", "id", "recipients", "replayed"}
	if got := jsonKeys(t, plan); !reflect.DeepEqual(got, wantPlan) {
		t.Errorf("plan keys = %v, want %v", got, wantPlan)
	}
	if got := jsonKeys(t, receipt); !reflect.DeepEqual(got, wantReceipt) {
		t.Errorf("receipt keys = %v, want %v", got, wantReceipt)
	}

	var m map[string]any
	roundTrip(t, plan, &m)
	att := m["attachments"].([]any)[0].(map[string]any)
	if att["name"] != "a.png" || att["mime"] != "image/png" || att["size"] != float64(3) {
		t.Errorf("attachment = %v, want snake_case name/mime/size", att)
	}
	m = nil
	roundTrip(t, receipt, &m)
	rr := m["recipients"].([]any)[0].(map[string]any)
	if rr["to"] != "+1" || rr["error"] != "boom" || rr["receipt"].(map[string]any)["id"] != "r1" {
		t.Errorf("recipient result = %v, want to/receipt/error with a snake_case receipt", rr)
	}
}

func TestPlanAndReceiptJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	plan := core.Plan{
		Action: "reply", Channel: core.ChannelMail, Account: "cl", Target: "a@b", Cc: []string{"c@d"},
		Subject: "Re: hi", Preview: "ok", Media: []string{"/f"},
		Attachments: []core.AttachmentInfo{{Name: "f", MIME: "text/plain", Size: 1}},
		Recipients:  []string{"a@b"}, FanoutPauseMin: time.Second, FanoutPauseMax: 2 * time.Second,
	}
	receipt := core.Receipt{ID: "r", Channel: core.ChannelMail, At: at, Replayed: true,
		Recipients: []core.RecipientResult{{To: "a@b", Receipt: core.Receipt{ID: "r", Channel: core.ChannelMail, At: at}}}}

	var gotPlan core.Plan
	var gotReceipt core.Receipt
	roundTrip(t, plan, &gotPlan)
	roundTrip(t, receipt, &gotReceipt)
	if !reflect.DeepEqual(gotPlan, plan) {
		t.Errorf("plan round trip = %+v, want %+v", gotPlan, plan)
	}
	if !reflect.DeepEqual(gotReceipt, receipt) {
		t.Errorf("receipt round trip = %+v, want %+v", gotReceipt, receipt)
	}
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	var m map[string]json.RawMessage
	roundTrip(t, v, &m)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func roundTrip(t *testing.T, in, out any) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}
