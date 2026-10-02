package matrix

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

func TestSendVoiceFlagsMSC3245AndDuration(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)

	path := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(path, 7*time.Second+250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.SendVoice(context.Background(), core.Outgoing{
		To:          []string{"!room:matrix.example.org"},
		Attachments: []string{path},
		Voice:       true,
	})
	if err != nil {
		t.Fatalf("SendVoice: %v", err)
	}
	if receipt.ID == "" {
		t.Error("Receipt.ID is empty")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 1 {
		t.Fatalf("sentEvents = %d, want 1", len(state.sentEvents))
	}
	content := decodeContent(t, state.sentEvents[0])
	if content.MsgType != event.MsgAudio {
		t.Errorf("MsgType = %q, want m.audio", content.MsgType)
	}
	if content.MSC3245Voice == nil {
		t.Error("org.matrix.msc3245.voice missing")
	}
	if content.MSC1767Audio == nil || content.MSC1767Audio.Duration != 7250 {
		t.Errorf("org.matrix.msc1767.audio = %+v, want duration 7250 ms", content.MSC1767Audio)
	}
	if content.Info == nil || content.Info.MimeType != "audio/ogg" {
		t.Errorf("Info = %+v, want audio/ogg", content.Info)
	}
}

func TestSendVoiceRejectsNonOggOpus(t *testing.T) {
	srv, state := newMediaFakeHomeserver(t, &mautrix.RespMediaConfig{UploadSize: 50 << 20})
	adapter := newMediaTestAdapter(t, srv)
	path := writeMatrixFile(t, "nota.mp3", []byte("ID3 not ogg at all"))

	_, err := adapter.SendVoice(context.Background(), core.Outgoing{To: []string{"!room:matrix.example.org"}, Attachments: []string{path}, Voice: true})
	if err == nil {
		t.Fatal("SendVoice(mp3) must fail")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sentEvents) != 0 || len(state.uploadedBodies) != 0 {
		t.Error("nothing may be uploaded or sent for a rejected file")
	}
}

func TestAttachmentFromContentVoice(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType:      event.MsgAudio,
		Body:         "voice.ogg",
		URL:          "mxc://matrix.example.org/v1",
		Info:         &event.FileInfo{MimeType: "audio/ogg", Size: 900},
		MSC3245Voice: &event.MSC3245Voice{},
		MSC1767Audio: &event.MSC1767Audio{Duration: 12400, Waveform: []int{0, 512, 1024, 2000}},
	}
	att, ok := attachmentFromContent(content)
	if !ok || !att.Voice || att.Duration != 12 {
		t.Fatalf("attachment = %+v, %v; want a 12 s voice note", att, ok)
	}
	if string(att.Waveform) != string([]byte{0, 50, 100, 100}) {
		t.Errorf("Waveform = %v", att.Waveform)
	}

	content.MSC3245Voice = nil
	if att, _ := attachmentFromContent(content); att.Voice || att.Duration != 0 {
		t.Errorf("without the voice flag %+v must be a plain audio file", att)
	}
}
