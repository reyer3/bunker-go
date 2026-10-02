package whatsapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

func TestSendVoiceBuildsPTTAudioMessage(t *testing.T) {
	cli := newFakeWAClient()
	cli.uploadResp = whatsmeow.UploadResponse{URL: "https://example/voice", DirectPath: "/v/x", FileLength: 400}
	cli.sendResp = whatsmeow.SendResponse{ID: "SENT-PTT", Timestamp: time.Unix(7000, 0)}
	a := NewAdapter("personal", cli, time.Millisecond)

	path := filepath.Join(t.TempDir(), "nota.ogg")
	if err := oggfixture.Write(path, 12*time.Second+400*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	receipt, err := a.SendVoice(context.Background(), core.Outgoing{
		To:          []string{"1234@s.whatsapp.net"},
		Attachments: []string{path},
		Voice:       true,
	})
	if err != nil {
		t.Fatalf("SendVoice() error = %v", err)
	}
	if receipt.ID == "" {
		t.Fatal("Receipt.ID is empty")
	}
	if len(cli.sent) != 1 {
		t.Fatalf("sent = %d messages, want 1", len(cli.sent))
	}
	aud := cli.sent[0].message.GetAudioMessage()
	if aud == nil {
		t.Fatalf("sent message has no AudioMessage: %+v", cli.sent[0].message)
	}
	if !aud.GetPTT() {
		t.Error("PTT = false, want true")
	}
	if aud.GetMimetype() != "audio/ogg; codecs=opus" {
		t.Errorf("Mimetype = %q", aud.GetMimetype())
	}
	if aud.GetSeconds() != 12 {
		t.Errorf("Seconds = %d, want 12", aud.GetSeconds())
	}
	if len(aud.GetWaveform()) != 0 {
		t.Errorf("Waveform = %v, want none (never faked)", aud.GetWaveform())
	}
	if cli.uploadedType != whatsmeow.MediaAudio {
		t.Errorf("Upload media type = %q, want MediaAudio", cli.uploadedType)
	}
}

func TestSendVoiceRejectsNonOggOpus(t *testing.T) {
	cli := newFakeWAClient()
	a := NewAdapter("personal", cli, time.Millisecond)
	path := writeFile(t, t.TempDir(), "nota.mp3", 64)

	_, err := a.SendVoice(context.Background(), core.Outgoing{To: []string{"1234@s.whatsapp.net"}, Attachments: []string{path}, Voice: true})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("SendVoice(mp3) error = %v, want one suggesting a conversion", err)
	}
	if len(cli.sent) != 0 {
		t.Errorf("sent = %d messages, want none", len(cli.sent))
	}
}

func TestToItemMarksPTTAsVoiceNote(t *testing.T) {
	chat := mustJID(t, "1234@s.whatsapp.net")
	mk := func(ptt bool) core.Item {
		return toItem("personal", &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: chat},
				ID:            "AUD1",
			},
			Message: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
				PTT:        &ptt,
				Mimetype:   strPtr("audio/ogg; codecs=opus"),
				Seconds:    func() *uint32 { s := uint32(12); return &s }(),
				Waveform:   []byte{1, 50, 100},
				DirectPath: strPtr("/v/a"),
			}},
		})
	}
	att := mk(true).Attachments[0]
	if !att.Voice || att.Duration != 12 || len(att.Waveform) != 3 {
		t.Errorf("voice attachment = %+v, want Voice, 12s, 3 bars", att)
	}
	plain := mk(false).Attachments[0]
	if plain.Voice || plain.Duration != 0 || plain.Waveform != nil {
		t.Errorf("non-PTT attachment = %+v, want a plain audio file", plain)
	}
}
