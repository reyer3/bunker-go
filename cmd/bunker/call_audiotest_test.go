package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reyer3/bunker-go/internal/channel/whatsapp"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

func audioTestCfg(names ...string) *config.Config {
	cfg := &config.Config{}
	for _, n := range names {
		cfg.Accounts = append(cfg.Accounts, config.Account{Channel: "whatsapp", Name: n})
	}
	cfg.Accounts = append(cfg.Accounts, config.Account{Channel: "mail", Name: "work"})
	return cfg
}

func runAudioTest(t *testing.T, cfg *config.Config, report whatsapp.AudioTestReport, args ...string) (code int, stdout, stderr string, acc config.Account, seconds int) {
	t.Helper()
	prev := whatsappAudioTestFunc
	whatsappAudioTestFunc = func(_ context.Context, a config.Account, s int) whatsapp.AudioTestReport {
		acc, seconds = a, s
		report.Account = a.Name
		return report
	}
	t.Cleanup(func() { whatsappAudioTestFunc = prev })
	var out, errb bytes.Buffer
	code = cmdCallAudioTest(context.Background(), cfg, args, &out, &errb)
	return code, out.String(), errb.String(), acc, seconds
}

func TestCallAudioTestDefaults(t *testing.T) {
	code, out, _, acc, seconds := runAudioTest(t, audioTestCfg("personal"), whatsapp.AudioTestReport{OK: true, MicPeak: 0.2})
	if code != 0 || acc.Name != "personal" || seconds != 3 {
		t.Fatalf("code=%d acc=%q seconds=%d", code, acc.Name, seconds)
	}
	if !strings.Contains(out, "result: ok") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestCallAudioTestJSONAndExitCode(t *testing.T) {
	report := whatsapp.AudioTestReport{OK: false, Hints: []string{"mic muted"}}
	code, out, _, acc, seconds := runAudioTest(t, audioTestCfg("a", "b"), report, "--account", "b", "--seconds", "5", "--json")
	if code != 1 || acc.Name != "b" || seconds != 5 {
		t.Fatalf("code=%d acc=%q seconds=%d", code, acc.Name, seconds)
	}
	var got whatsapp.AudioTestReport
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Account != "b" || len(got.Hints) != 1 {
		t.Fatalf("json = %q (%v)", out, err)
	}
}

func TestCallAudioTestErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  *config.Config
		args []string
		want string
	}{
		"ambiguous":  {audioTestCfg("a", "b"), nil, "pick one with --account"},
		"unknown":    {audioTestCfg("a"), []string{"--account", "zzz"}, `no "whatsapp" account named "zzz"`},
		"no account": {&config.Config{}, nil, "no whatsapp account"},
		"seconds":    {audioTestCfg("a"), []string{"--seconds", "0"}, "--seconds must be between"},
	} {
		code, _, stderr, _, _ := runAudioTest(t, tc.cfg, whatsapp.AudioTestReport{}, tc.args...)
		if code != 1 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%s: code=%d stderr=%q, want %q", name, code, stderr, tc.want)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := cmdCallAudioTest(context.Background(), audioTestCfg("a"), []string{"extra"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: bunker call audio-test") {
		t.Fatalf("positional: code=%d stderr=%q", code, stderr.String())
	}
}

func TestFormatCallShowsAudioError(t *testing.T) {
	c := core.Call{ID: "C1", Channel: core.ChannelWhatsApp, Account: "p", Peer: "51@s.whatsapp.net", Direction: core.CallOutgoing, State: core.CallStateConnecting, AudioError: "sin medios"}
	if got := formatCall(c); !strings.HasSuffix(got, "[sin audio: sin medios]") {
		t.Fatalf("formatCall = %q", got)
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"audio_error":"sin medios"`) {
		t.Fatalf("json = %s", b)
	}
	b, _ = json.Marshal(core.Call{ID: "C2"})
	if strings.Contains(string(b), "audio_error") {
		t.Fatalf("empty audio_error serialized: %s", b)
	}
}
