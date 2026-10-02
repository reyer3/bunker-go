package whatsapp

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/reyer3/bunker-go/internal/core"
)

// mediaMeta is the subset of fields every media message type exposes that
// this adapter cares about: enough to describe an Attachment without
// downloading the bytes.
type mediaMeta struct {
	mime    string
	size    uint64
	ref     string // DirectPath, or the URL when DirectPath is unset.
	name    string
	caption string
	// thumb is the preview embedded in the message itself (see
	// thumbnail.go), unvalidated; nil when the sender attached none.
	thumb []byte
	// voice, seconds and waveform describe a push-to-talk audio message.
	voice    bool
	seconds  int
	waveform []byte
}

// bodyAndMedia extracts the text body and, when present, one media
// attachment's metadata and quoted-reply context out of a raw WhatsApp
// message. WhatsApp messages carry exactly one payload kind per message.
func bodyAndMedia(msg *waE2E.Message) (body string, media *mediaMeta, ctx *waE2E.ContextInfo) {
	if msg == nil {
		return "", nil, nil
	}
	if msg.Conversation != nil {
		return msg.GetConversation(), nil, nil
	}
	if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText(), nil, ext.GetContextInfo()
	}
	if img := msg.GetImageMessage(); img != nil {
		m := &mediaMeta{mime: img.GetMimetype(), size: img.GetFileLength(), ref: mediaRef(img.GetDirectPath(), img.GetURL()), name: "image", caption: img.GetCaption(), thumb: img.GetJPEGThumbnail()}
		return img.GetCaption(), m, img.GetContextInfo()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		m := &mediaMeta{mime: vid.GetMimetype(), size: vid.GetFileLength(), ref: mediaRef(vid.GetDirectPath(), vid.GetURL()), name: "video", caption: vid.GetCaption(), thumb: vid.GetJPEGThumbnail()}
		return vid.GetCaption(), m, vid.GetContextInfo()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		name := doc.GetFileName()
		if name == "" {
			name = doc.GetTitle()
		}
		m := &mediaMeta{mime: doc.GetMimetype(), size: doc.GetFileLength(), ref: mediaRef(doc.GetDirectPath(), doc.GetURL()), name: name, caption: doc.GetCaption(), thumb: doc.GetJPEGThumbnail()}
		return doc.GetCaption(), m, doc.GetContextInfo()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		m := &mediaMeta{mime: aud.GetMimetype(), size: aud.GetFileLength(), ref: mediaRef(aud.GetDirectPath(), aud.GetURL()), name: "audio", voice: aud.GetPTT(), seconds: int(aud.GetSeconds()), waveform: aud.GetWaveform()}
		return "", m, aud.GetContextInfo()
	}
	if sticker := msg.GetStickerMessage(); sticker != nil {
		m := &mediaMeta{mime: sticker.GetMimetype(), size: sticker.GetFileLength(), ref: mediaRef(sticker.GetDirectPath(), sticker.GetURL()), name: "sticker", thumb: sticker.GetPngThumbnail()}
		return "", m, sticker.GetContextInfo()
	}
	return "", nil, nil
}

func mediaRef(directPath, url string) string {
	if directPath != "" {
		return directPath
	}
	return url
}

// toItem converts an incoming whatsmeow message event into a core.Item.
// account is the configured account name (config.Account.Name), never
// derived from the network payload.
func toItem(account string, evt *events.Message) core.Item {
	body, media, ctx := bodyAndMedia(evt.Message)

	item := core.Item{
		ID:         itemID(account, evt.Info.Chat.String(), string(evt.Info.ID)),
		Channel:    core.ChannelWhatsApp,
		Account:    account,
		Thread:     evt.Info.Chat.String(),
		ThreadName: evt.Info.PushName,
		From:       core.Address{ID: evt.Info.Sender.String(), Name: evt.Info.PushName},
		Body:       body,
		Unread:     !evt.Info.IsFromMe,
		FromMe:     evt.Info.IsFromMe,
		Timestamp:  evt.Info.Timestamp,
		Meta:       map[string]string{},
	}

	if media != nil {
		// A bad thumbnail only costs the inline preview (the TUI falls
		// back to downloading), never the message, so it is logged and
		// dropped rather than failing the item.
		thumb, err := validThumbnail(media.thumb)
		if err != nil {
			logf("%v; ignoring the thumbnail of %s", err, item.ID)
		}
		att := core.Attachment{
			Name:      media.name,
			MIME:      media.mime,
			Size:      int64(media.size),
			Ref:       media.ref,
			Thumbnail: thumb,
		}
		if media.voice {
			att.Voice, att.Duration = true, media.seconds
			// Waveforms are 64 bytes; anything much larger is not one.
			if len(media.waveform) <= 256 {
				att.Waveform = media.waveform
			}
		}
		item.Attachments = append(item.Attachments, att)
	}

	if ctx != nil && ctx.GetStanzaID() != "" {
		item.Meta["wa_quoted_stanza_id"] = ctx.GetStanzaID()
		if p := ctx.GetParticipant(); p != "" {
			item.Meta["wa_quoted_participant"] = p
		}
	}

	return item
}

// isSurfaceable reports whether item carries anything a user should see:
// real text or a media attachment. A newly linked device receives plenty
// of *events.Message traffic that is not user content at all - app-state
// key distribution, history-sync notifications, sender-key distribution -
// and bodyAndMedia extracts neither text nor media from any of those, so
// they fall out here without special-casing each protobuf kind.
//
// WhatsApp reactions, message edits and revokes also arrive as
// *events.Message and produce no body/media through toItem, but
// Adapter.handleEvent intercepts them before toItem/isSurfaceable ever
// run (see handleEditOrRevoke/handleReaction, S2): they are applied as
// operations on the existing target Item instead of being dropped here.
func isSurfaceable(item core.Item) bool {
	return item.Body != "" || len(item.Attachments) > 0
}
