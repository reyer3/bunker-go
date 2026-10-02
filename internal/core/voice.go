package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// voiceMIME is the type a voice note is sent as: WhatsApp and Matrix
// clients only play a note back inline when it is Opus in an Ogg container.
const voiceMIME = "audio/ogg; codecs=opus"

type voiceCtx struct{}

// WithVoice returns ctx marking the next Reply as a voice note. Like the
// idempotency key it travels in the context so Reply keeps its signature
// across every Backend and fake; only the RPC client (which puts it on the
// wire) and Service (which honors it) look at it.
func WithVoice(ctx context.Context) context.Context {
	return context.WithValue(ctx, voiceCtx{}, true)
}

// IsVoice reports whether WithVoice marked ctx.
func IsVoice(ctx context.Context) bool {
	v, _ := ctx.Value(voiceCtx{}).(bool)
	return v
}

// prepareVoice validates a voice note request and describes it for the
// plan. It fails loudly on everything a channel could not deliver as one:
// not exactly one file, text alongside (neither channel lets a voice note
// carry a caption), several recipients (a voice note is never a broadcast),
// a file that is not Ogg Opus, or one over the adapter's size limit.
func prepareVoice(policy AttachmentPolicy, out Outgoing) ([]AttachmentInfo, error) {
	if len(out.Attachments) != 1 {
		return nil, fmt.Errorf("core: a voice note needs exactly one audio file, got %d", len(out.Attachments))
	}
	if strings.TrimSpace(out.Body) != "" {
		return nil, fmt.Errorf("core: a voice note cannot carry text; send the text as its own message")
	}
	if len(out.Cc) > 0 {
		return nil, fmt.Errorf("core: a voice note has no Cc: %w", ErrUnsupported)
	}
	if len(out.To) != 1 {
		return nil, fmt.Errorf("core: a voice note goes to exactly one recipient, got %d", len(out.To))
	}
	path := out.Attachments[0]
	info, err := inspectAttachment(path)
	if err != nil {
		return nil, err
	}
	d, err := OggOpusDuration(path)
	if err != nil {
		return nil, err
	}
	max, ok := policy.MaxBytes["audio/ogg"]
	if !ok {
		max, ok = policy.MaxBytes[AnyMIME]
	}
	if !ok {
		return nil, fmt.Errorf("core: this channel does not accept %s voice notes", voiceMIME)
	}
	if info.Size > max {
		return nil, fmt.Errorf("core: voice note %q is %d bytes, over the %d byte limit", info.Name, info.Size, max)
	}
	info.MIME = voiceMIME
	info.Voice = true
	info.DurationMS = d.Milliseconds()
	return []AttachmentInfo{info}, nil
}

// deliverVoice is the voice branch of send and reply: validate, build the
// plan, and (unless dryRun) send through the adapter's VoiceSender.
func (s *Service) deliverVoice(ctx context.Context, adapter Adapter, plan Plan, out Outgoing, thread, to, verb string, dryRun bool) (Plan, Receipt, error) {
	vs, ok := adapter.(VoiceSender)
	if !ok {
		return Plan{}, Receipt{}, fmt.Errorf("core: adapter %s/%s cannot send voice notes: %w", out.Channel, out.Account, ErrUnsupported)
	}
	infos, err := prepareVoice(vs.AttachmentPolicy(), out)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: %s: %w", verb, err)
	}
	plan.Attachments = infos
	plan.Voice = true
	if dryRun {
		return plan, Receipt{}, nil
	}
	receipt, err := vs.SendVoice(ctx, out)
	if err != nil {
		return Plan{}, Receipt{}, fmt.Errorf("core: %s voice note failed: %w", verb, err)
	}
	s.storeSentItem(ctx, out.Channel, out.Account, thread, to, out.Subject, "", plan.Attachments, receipt)
	return plan, receipt, nil
}

// VoiceDuration is the duration of a voice AttachmentInfo.
func (a AttachmentInfo) VoiceDuration() time.Duration {
	return time.Duration(a.DurationMS) * time.Millisecond
}
