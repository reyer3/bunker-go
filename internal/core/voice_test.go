package core_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// spyVoiceAdapter is a MediaSender that also implements core.VoiceSender.
type spyVoiceAdapter struct {
	spyMediaAdapter
	voiceCalls int
	lastVoice  core.Outgoing
}

func (s *spyVoiceAdapter) SendVoice(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	s.voiceCalls++
	s.lastVoice = out
	return core.Receipt{ID: "voice-1", Channel: s.channel, At: time.Unix(4, 0)}, nil
}

func voiceFixture(t *testing.T, d time.Duration) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(p, d); err != nil {
		t.Fatal(err)
	}
	return p
}

func newVoiceService(t *testing.T) (*core.Service, *spyVoiceAdapter, *memStore) {
	t.Helper()
	item := core.Item{ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal", Thread: "1234@s.whatsapp.net", From: core.Address{ID: "1234@s.whatsapp.net"}}
	store := newMemStore(item)
	reg := core.NewRegistry()
	spy := &spyVoiceAdapter{spyMediaAdapter: spyMediaAdapter{
		spyAdapter: spyAdapter{channel: core.ChannelWhatsApp, account: "personal"},
		policy:     core.AttachmentPolicy{MaxBytes: map[string]int64{"audio/ogg": 1 << 20}},
	}}
	reg.Register(spy)
	return core.NewService(store, reg), spy, store
}

func TestSendVoiceDryRunPlanShowsVoiceAndDuration(t *testing.T) {
	svc, spy, _ := newVoiceService(t)
	path := voiceFixture(t, 12*time.Second)
	plan, receipt, err := svc.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"1234@s.whatsapp.net"},
		Attachments: []string{path}, Voice: true,
	}, true)
	if err != nil {
		t.Fatalf("Send dry-run: %v", err)
	}
	if spy.voiceCalls != 0 || spy.sendMediaCalls != 0 || receipt.ID != "" {
		t.Fatalf("dry-run reached the adapter: voice=%d media=%d receipt=%+v", spy.voiceCalls, spy.sendMediaCalls, receipt)
	}
	if !plan.Voice || len(plan.Attachments) != 1 || !plan.Attachments[0].Voice || plan.Attachments[0].DurationMS != 12000 {
		t.Fatalf("plan = %+v, want a 12 s voice note", plan)
	}
}

func TestSendVoiceRoutesToVoiceSenderAndStoresVoiceItem(t *testing.T) {
	svc, spy, store := newVoiceService(t)
	path := voiceFixture(t, 5*time.Second)
	_, receipt, err := svc.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"1234@s.whatsapp.net"},
		Attachments: []string{path}, Voice: true,
	}, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if spy.voiceCalls != 1 || spy.sendMediaCalls != 0 || spy.sendCalls != 0 {
		t.Fatalf("voice=%d media=%d send=%d, want 1/0/0", spy.voiceCalls, spy.sendMediaCalls, spy.sendCalls)
	}
	got, err := store.Get(context.Background(), receipt.ID)
	if err != nil || len(got.Attachments) != 1 || !got.Attachments[0].Voice || got.Attachments[0].Duration != 5 {
		t.Fatalf("stored sent item = %+v, %v; want a 5 s voice attachment", got, err)
	}
}

func TestReplyVoiceViaContext(t *testing.T) {
	svc, spy, _ := newVoiceService(t)
	path := voiceFixture(t, 3*time.Second)
	plan, _, err := svc.Reply(core.WithVoice(context.Background()), "whatsapp:personal:1", "", nil, []string{path}, false)
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if !plan.Voice || spy.voiceCalls != 1 || spy.lastVoice.ReplyTo != "whatsapp:personal:1" {
		t.Fatalf("plan=%+v voiceCalls=%d lastVoice=%+v", plan, spy.voiceCalls, spy.lastVoice)
	}
}

func TestSendVoiceValidation(t *testing.T) {
	svc, spy, _ := newVoiceService(t)
	good := voiceFixture(t, time.Second)
	notOgg := writeTestFile(t, "x.mp3", []byte("ID3 definitely not ogg"))
	base := core.Outgoing{Channel: core.ChannelWhatsApp, Account: "personal", To: []string{"1234@s.whatsapp.net"}, Voice: true}

	cases := map[string]struct {
		mut  func(o *core.Outgoing)
		want string
	}{
		"no file":   {func(o *core.Outgoing) {}, "exactly one"},
		"two files": {func(o *core.Outgoing) { o.Attachments = []string{good, good} }, "exactly one"},
		"with text": {func(o *core.Outgoing) { o.Attachments = []string{good}; o.Body = "hola" }, "cannot carry text"},
		"two targets": {func(o *core.Outgoing) {
			o.Attachments = []string{good}
			o.To = []string{"1@s.whatsapp.net", "2@s.whatsapp.net"}
		}, "exactly one recipient"},
		"not ogg": {func(o *core.Outgoing) { o.Attachments = []string{notOgg} }, "ffmpeg"},
	}
	for name, tc := range cases {
		out := base
		tc.mut(&out)
		_, _, err := svc.Send(context.Background(), out, true)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
	if spy.voiceCalls != 0 {
		t.Errorf("a rejected voice note reached the adapter")
	}
	out := base
	out.Attachments = []string{notOgg}
	if _, _, err := svc.Send(context.Background(), out, true); !errors.Is(err, core.ErrNotOggOpus) {
		t.Errorf("non-Ogg error must wrap ErrNotOggOpus, got %v", err)
	}
}

func TestSendVoiceUnsupportedChannel(t *testing.T) {
	store := newMemStore()
	reg := core.NewRegistry()
	reg.Register(&spyMediaAdapter{spyAdapter: spyAdapter{channel: core.ChannelMail, account: "cl"}, policy: imagePolicy(1 << 20)})
	svc := core.NewService(store, reg)
	_, _, err := svc.Send(context.Background(), core.Outgoing{
		Channel: core.ChannelMail, Account: "cl", To: []string{"a@x.cl"},
		Attachments: []string{voiceFixture(t, time.Second)}, Voice: true,
	}, true)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported (never a plain file)", err)
	}
}
