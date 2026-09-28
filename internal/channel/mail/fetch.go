package mail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// Fetch implements core.Fetcher: it re-fetches one message by UID and
// returns it with a full body and attachment metadata, which Run's
// initial sync/IDLE upserts don't populate. It always uses BODY.PEEK, so
// it never marks a message \Seen.
func (a *Adapter) Fetch(ctx context.Context, id string) (core.Item, error) {
	account, folder, uidValidity, uid, err := parseItemID(id)
	if err != nil {
		return core.Item{}, err
	}
	if account != a.cfg.Name {
		return core.Item{}, fmt.Errorf("mail: item %q is not for account %q: %w", id, a.cfg.Name, core.ErrNotFound)
	}

	client, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return core.Item{}, fmt.Errorf("mail: fetch %s: %w", id, err)
	}
	defer client.Close()

	folders, err := discoverFolders(ctx, client, a.cfg)
	if err != nil {
		return core.Item{}, fmt.Errorf("mail: fetch %s: %w", id, err)
	}
	mailbox := folders.Resolve(folder)

	mbox, err := client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return core.Item{}, fmt.Errorf("mail: fetch %s: select %s: %w", id, mailbox, err)
	}
	if mbox.UIDValidity != uidValidity {
		return core.Item{}, fmt.Errorf("mail: fetch %s: mailbox UIDVALIDITY changed (%d != %d): %w", id, mbox.UIDValidity, uidValidity, core.ErrNotFound)
	}

	fetchOptions := &imap.FetchOptions{
		UID:      true,
		Flags:    true,
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"References"}, Peek: true},
			{Peek: true}, // whole raw message, for body text and attachments
		},
	}
	messages, err := client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil {
		return core.Item{}, fmt.Errorf("mail: fetch %s: %w", id, err)
	}
	if len(messages) == 0 {
		return core.Item{}, fmt.Errorf("mail: fetch %s: %w", id, core.ErrNotFound)
	}
	msg := messages[0]

	item := a.buildItem(msg, folders, uidValidity, folder)
	if a.cfg.Gmail {
		// T14(a): see fetchAndUpsert's identical comment — X-GM-LABELS
		// needs the raw connection, and a failure here must never fail
		// this Fetch.
		labels, err := a.fetchGmailLabelsRaw(ctx, []imap.UID{uid})
		if err != nil {
			log.Printf("mail: fetch: gmail X-GM-LABELS fetch for %q failed, leaving Labels as synced: %v", a.cfg.Name, err)
		} else if l, ok := labels[uid]; ok {
			item.Labels = l
		}
	}

	var raw []byte
	for _, section := range msg.BodySection {
		if section.Section != nil && len(section.Section.HeaderFields) > 0 {
			continue // the References-only header section, already used by buildItem
		}
		raw = section.Bytes
	}
	if len(raw) > 0 {
		body, attachments, err := parseBody(raw)
		if err != nil {
			return core.Item{}, fmt.Errorf("mail: fetch %s: parse body: %w", id, err)
		}
		item.Body = body
		item.Attachments = attachments
	}

	return item, nil
}

// parseBody walks a raw RFC 5322 message and returns its best-effort
// plain-text body (text/plain preferred, text/html rendered via
// HTMLToText as a fallback) plus metadata for every attachment part.
func parseBody(raw []byte) (body string, attachments []core.Attachment, err error) {
	reader, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !gomessage.IsUnknownCharset(err) {
		return "", nil, fmt.Errorf("read message: %w", err)
	}
	defer reader.Close()

	var plainText, htmlText string
	haveText := false

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomessage.IsUnknownCharset(err) {
			return "", nil, fmt.Errorf("read part: %w", err)
		}

		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			contentType, _, _ := h.ContentType()
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				return "", nil, fmt.Errorf("read inline part: %w", readErr)
			}
			switch {
			case strings.EqualFold(contentType, "text/plain") && !haveText:
				plainText = string(data)
				haveText = true
			case strings.EqualFold(contentType, "text/html") && htmlText == "":
				htmlText = string(data)
			}
		case *gomail.AttachmentHeader:
			contentType, _, _ := h.ContentType()
			filename, _ := h.Filename()
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				return "", nil, fmt.Errorf("read attachment part: %w", readErr)
			}
			attachments = append(attachments, core.Attachment{
				Name: filename,
				MIME: contentType,
				Size: int64(len(data)),
				Ref:  fmt.Sprintf("part-%d", len(attachments)+1),
			})
		}
	}

	if haveText {
		return plainText, attachments, nil
	}
	if htmlText != "" {
		return HTMLToText(htmlText), attachments, nil
	}
	return "", attachments, nil
}
