package mail

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// DownloadAttachment implements core.AttachmentDownloader: it re-fetches
// item's raw message by UID over IMAP (the same BODY.PEEK round trip
// Fetch makes, so this never marks anything \Seen either) and returns
// the index-th attachment part's raw bytes. Mail keeps no attachment
// bytes of its own between calls, so every download re-reads the source
// message from the server.
func (a *Adapter) DownloadAttachment(ctx context.Context, item core.Item, index int) (io.ReadCloser, error) {
	if index < 0 || index >= len(item.Attachments) {
		return nil, fmt.Errorf("mail: download %s: attachment index %d out of range: %w", item.ID, index, core.ErrNotFound)
	}

	account, uidValidity, uid, err := parseItemID(item.ID)
	if err != nil {
		return nil, err
	}
	if account != a.cfg.Name {
		return nil, fmt.Errorf("mail: item %q is not for account %q: %w", item.ID, a.cfg.Name, core.ErrNotFound)
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return nil, fmt.Errorf("mail: download %s: %w", item.ID, err)
	}
	defer client.Close()

	mbox, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("mail: download %s: select INBOX: %w", item.ID, err)
	}
	if mbox.UIDValidity != uidValidity {
		return nil, fmt.Errorf("mail: download %s: mailbox UIDVALIDITY changed (%d != %d): %w", item.ID, mbox.UIDValidity, uidValidity, core.ErrNotFound)
	}

	fetchOptions := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
	messages, err := client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil {
		return nil, fmt.Errorf("mail: download %s: %w", item.ID, err)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("mail: download %s: %w", item.ID, core.ErrNotFound)
	}

	var raw []byte
	for _, section := range messages[0].BodySection {
		raw = section.Bytes
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("mail: download %s: empty message body", item.ID)
	}

	data, err := attachmentPartAt(raw, index)
	if err != nil {
		return nil, fmt.Errorf("mail: download %s: %w", item.ID, err)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// attachmentPartAt walks raw's MIME parts in exactly the order parseBody
// (fetch.go) builds Item.Attachments from — only counting
// *gomail.AttachmentHeader parts — and returns the index-th one's raw
// bytes, so an index taken from a stored Item.Attachments slice always
// lands on the matching part here.
func attachmentPartAt(raw []byte, index int) ([]byte, error) {
	reader, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !gomessage.IsUnknownCharset(err) {
		return nil, fmt.Errorf("read message: %w", err)
	}
	defer reader.Close()

	n := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomessage.IsUnknownCharset(err) {
			return nil, fmt.Errorf("read part: %w", err)
		}
		if _, ok := part.Header.(*gomail.AttachmentHeader); !ok {
			continue
		}
		if n == index {
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				return nil, fmt.Errorf("read attachment part: %w", readErr)
			}
			return data, nil
		}
		n++
	}
	return nil, fmt.Errorf("attachment index %d not found (message has %d attachments): %w", index, n, core.ErrNotFound)
}
