package mail

import (
	"context"

	"github.com/reyer3/bunker-go/internal/core"
)

// maxAttachmentBytes caps both each attachment and their sum in one mail.
// Base64 inflates attachments by ~4/3, so 18 MB of files stays under
// Gmail's 25 MB message limit; Dovecot/Postfix defaults are not lower.
const maxAttachmentBytes = 18 << 20

// AttachmentPolicy implements core.MediaSender: mail carries any file type,
// bounded by the message size limit.
func (a *Adapter) AttachmentPolicy() core.AttachmentPolicy {
	return core.AttachmentPolicy{
		MaxBytes:      map[string]int64{core.AnyMIME: maxAttachmentBytes},
		MaxTotalBytes: maxAttachmentBytes,
	}
}

// SendMedia implements core.MediaSender. Send already renders
// Outgoing.Attachments as multipart/mixed parts; Service has validated them
// against AttachmentPolicy before this runs.
func (a *Adapter) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	return a.Send(ctx, out)
}
