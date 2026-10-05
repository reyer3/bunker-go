package whatsapp

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// whatsappVoiceMIME is the exact Mimetype WhatsApp clients expect on a
// push-to-talk note; anything else shows up as a plain audio file.
const whatsappVoiceMIME = "audio/ogg; codecs=opus"

// buildVoiceMessage uploads path (an Ogg Opus file) as MediaAudio and
// wraps it in a PTT AudioMessage carrying its length in whole seconds. The
// Waveform field is left empty on purpose: computing one needs an Opus
// decoder, and a made-up waveform would misrepresent the audio. Clients
// draw a flat placeholder without it.
func (a *Adapter) buildVoiceMessage(ctx context.Context, path string) (*waE2E.Message, error) {
	d, err := core.OggOpusDuration(path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: voice note: %w", err)
	}
	data, err := readMediaFile(path, maxMediaBytes)
	if err != nil {
		return nil, err
	}
	up, err := a.cli.Upload(ctx, data, whatsmeow.MediaAudio)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: upload media %q: %w", path, err)
	}
	seconds := uint32((d.Milliseconds() + 500) / 1000)
	if seconds == 0 {
		seconds = 1
	}
	return &waE2E.Message{
		AudioMessage: &waE2E.AudioMessage{
			PTT:           ptrBool(true),
			Mimetype:      ptrString(whatsappVoiceMIME),
			Seconds:       &seconds,
			URL:           ptrString(up.URL),
			DirectPath:    ptrString(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    ptrUint64(up.FileLength),
		},
	}, nil
}

// SendVoice delivers out's single Ogg Opus attachment as a voice note
// (core.VoiceSender). It shows "recording audio" instead of "typing" while
// it waits, observes the same send pacing as every other message, and
// quotes out.ReplyTo when set. One note per call, never a batch.
func (a *Adapter) SendVoice(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	if len(out.Attachments) != 1 {
		return core.Receipt{}, fmt.Errorf("whatsapp: send voice note: need exactly one audio file, got %d", len(out.Attachments))
	}
	jid, err := a.resolveTarget(ctx, out)
	if err != nil {
		return core.Receipt{}, err
	}
	ctxInfo, err := a.outgoingContext(out)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("whatsapp: send voice note: %w", err)
	}
	var receipt core.Receipt
	var sent *waE2E.Message
	err = a.withHumanEmulationAs(ctx, jid, "", true, types.ChatPresenceMediaAudio, func() error {
		msg, err := a.buildVoiceMessage(ctx, out.Attachments[0])
		if err != nil {
			return err
		}
		if ctxInfo != nil {
			setContextInfo(msg, ctxInfo)
		}
		if err := a.waitForPacing(ctx); err != nil {
			return fmt.Errorf("whatsapp: send voice note: %w", err)
		}
		resp, err := a.cli.SendMessage(ctx, jid, msg)
		if err != nil {
			return fmt.Errorf("whatsapp: send voice note: %w", err)
		}
		sent = msg
		receipt = a.sentReceipt(ctx, jid, resp)
		return nil
	})
	if err != nil {
		return core.Receipt{}, err
	}
	a.persistSentDescriptors(ctx, receipt.ID, []*waE2E.Message{sent})
	return receipt, nil
}
