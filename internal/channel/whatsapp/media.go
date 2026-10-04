package whatsapp

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"

	"github.com/reyer3/bunker-go/internal/core"
)

// maxImageBytes is WhatsApp's documented upload limit for an image
// message. It backs both enforcement points that use it: AttachmentPolicy
// (core.Service validates against it before SendMedia is ever called, on
// dry-run send/reply too) and buildImageMessage's own on-disk-size check
// below, which is what actually protects PostStatus's media branch — the
// only caller that never goes through core.Service's attachment
// validation, since a status's Media is a single core.Status.Media path,
// not an Outgoing.Attachments list.
const maxImageBytes = 16 * 1024 * 1024 // 16 MB

// AttachmentPolicy declares what this adapter accepts for
// Outgoing.Attachments: image/png, image/jpeg and image/webp, plus (T16,
// see mediatypes.go) video and audio, all up to maxImageBytes each, and
// every other type as a document up to maxDocumentBytes via core.AnyMIME.
// core.Service validates every attachment against this before ever
// calling SendMedia, on dry-run too, so the type/size rule lives here
// instead of in the CLI.
func (a *Adapter) AttachmentPolicy() core.AttachmentPolicy {
	return mediaAttachmentPolicy()
}

// buildImageMessage reads the image at path, uploads it to WhatsApp and
// wraps the result in a waE2E.Message carrying caption. It is shared by
// PostStatus's media branch and SendMedia, the two places that turn a
// local file into an outgoing WhatsApp image.
func (a *Adapter) buildImageMessage(ctx context.Context, path, caption string) (*waE2E.Message, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read media %q: %w", path, err)
	}
	if info.Size() > maxImageBytes {
		return nil, fmt.Errorf("whatsapp: media %q is %d bytes, over the %d byte WhatsApp image limit", path, info.Size(), int64(maxImageBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: read media %q: %w", path, err)
	}
	up, err := a.cli.Upload(ctx, data, whatsmeow.MediaImage)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: upload media %q: %w", path, err)
	}
	return &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption:       ptrString(caption),
			Mimetype:      ptrString(http.DetectContentType(data)),
			URL:           ptrString(up.URL),
			DirectPath:    ptrString(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    ptrUint64(up.FileLength),
		},
	}, nil
}

// SendMedia delivers one WhatsApp message per file in out.Attachments, in
// order, to the same target Send would resolve. Each file's own extension
// picks its message type — image, video, audio or (everything else)
// document — via buildMediaMessage (mediatypes.go). out.Body captions
// only the first attachment (dropped entirely when that attachment is
// audio, which WhatsApp's protocol has no caption field for at all):
// WhatsApp has no single message that carries several attachments, so a
// caption on every one would repeat it, and a separate text message first
// would split the caption from what it describes. When out.ReplyTo is set
// (a reply carrying attachments), the same first attachment also carries
// the ContextInfo quoting the original message (setContextInfo), built the
// same way a plain text reply does (buildQuoteContext) — quoting every
// attachment would repeat the quote the way a caption on every one would
// repeat the caption. Each attachment still observes the adapter's send
// pacing (see waitForPacing), so multiple attachments are not a bulk API
// either. core.Service picks this method over plain Send whenever
// out.Attachments is non-empty; a direct call with none falls back to
// Send.
func (a *Adapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	if len(out.Attachments) == 0 {
		return a.Send(ctx, out)
	}

	jid, err := a.resolveTarget(ctx, out)
	if err != nil {
		return core.Receipt{}, err
	}

	var quoteCtx *waE2E.ContextInfo
	if out.ReplyTo != "" {
		quoteCtx, err = a.buildQuoteContext(out.ReplyTo)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("whatsapp: send media: %w", err)
		}
	}

	// The human-emulation choreography (T13b) brackets the WHOLE batch
	// once: a caption is typed once, then every image goes out, not one
	// composing/paused cycle per image.
	var receipt core.Receipt
	var sent []*waE2E.Message
	err = a.withHumanEmulation(ctx, jid, out.Body, true, func() error {
		for i, path := range out.Attachments {
			caption := ""
			if i == 0 {
				caption = out.Body
			}
			msg, err := a.buildMediaMessage(ctx, path, caption)
			if err != nil {
				return fmt.Errorf("whatsapp: send media: %w", err)
			}
			if i == 0 && quoteCtx != nil {
				setContextInfo(msg, quoteCtx)
			}
			if err := a.waitForPacing(ctx); err != nil {
				return fmt.Errorf("whatsapp: send media: %w", err)
			}
			resp, err := a.cli.SendMessage(ctx, jid, msg)
			if err != nil {
				return fmt.Errorf("whatsapp: send media: %w", err)
			}
			sent = append(sent, msg)
			receipt = core.Receipt{
				ID:      itemID(a.account, jid.String(), string(resp.ID)),
				Channel: core.ChannelWhatsApp,
				At:      resp.Timestamp,
			}
			// AudioMessage has no Caption: send the body as its own text
			// message rather than dropping it.
			if i == 0 && caption != "" && msg.GetAudioMessage() != nil {
				if err := a.waitForPacing(ctx); err != nil {
					return fmt.Errorf("whatsapp: send media: %w", err)
				}
				if _, err := a.cli.SendMessage(ctx, jid, &waE2E.Message{Conversation: &caption}); err != nil {
					return fmt.Errorf("whatsapp: send media: audio caption: %w", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return core.Receipt{}, err
	}
	a.persistSentDescriptors(ctx, receipt.ID, sent)
	return receipt, nil
}
