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
	mailbox := mailboxFor(folders, folder)

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
		labels, err := a.fetchGmailLabelsRaw(ctx, mailbox, []imap.UID{uid})
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
		body, attachments, calendars, err := walkMessage(raw, false)
		if err != nil {
			return core.Item{}, fmt.Errorf("mail: fetch %s: parse body: %w", id, err)
		}
		item.Body = body
		item.Attachments = attachments
		setMeeting(&item, calendars)
	}

	return item, nil
}

// walkMessage walks a raw RFC 5322 message and returns its best-effort
// plain-text body (text/plain preferred, text/html rendered via
// HTMLToText as a fallback), metadata for every attachment part, and the
// text of every calendar part (a text/calendar alternative or an .ics
// attachment), which setMeeting turns into the invitation the message
// carries.
//
// inlineFilename picks a filename for an inline part: the
// Content-Disposition filename param first, the Content-Type name param
// second — the same order AttachmentHeader.Filename uses, so both part
// kinds render the same way in the client.
func inlineFilename(contentType func() (string, map[string]string, error),
	disposition func() (string, map[string]string, error)) string {
	if _, params, err := disposition(); err == nil {
		if name, ok := params["filename"]; ok {
			return name
		}
	}
	if _, params, err := contentType(); err == nil {
		return params["name"]
	}
	return ""
}

// isInlineImage says whether a MIME content type is an image the client
// can render on its own (PNG, JPEG, GIF, WebP...). Only ever true for
// leaf parts, never for a multipart wrapper.
func isInlineImage(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), "image/")
}

// inlineImageName builds a filename for a CID-embedded image whose part
// carries none, so the client can still save it with a sensible string
// (n is the 1-based position the part takes in Item.Attachments).
func inlineImageName(contentType string, n int) string {
	ext := "bin"
	switch strings.ToLower(contentType) {
	case "image/png":
		ext = "png"
	case "image/jpeg":
		ext = "jpg"
	case "image/gif":
		ext = "gif"
	case "image/webp":
		ext = "webp"
	case "image/bmp":
		ext = "bmp"
	case "image/svg+xml":
		ext = "svg"
	case "image/avif":
		ext = "avif"
	}
	return fmt.Sprintf("cid-%d.%s", n, ext)
}

// truncated says raw was cut short on purpose (sync's bounded body fetch,
// bodytext.go): the MIME structure then ends abruptly, so a read error
// stops the walk and keeps the text gathered so far instead of failing,
// and the attachments are meaningless (their sizes are cut too) and must
// not be used.
func walkMessage(raw []byte, truncated bool) (body string, attachments []core.Attachment, calendars []string, err error) {
	reader, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !gomessage.IsUnknownCharset(err) {
		return "", nil, nil, fmt.Errorf("read message: %w", err)
	}
	defer reader.Close()

	var plainText, htmlText string
	haveText := false
	stop := false

	for !stop {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomessage.IsUnknownCharset(err) {
			if truncated {
				break
			}
			return "", nil, nil, fmt.Errorf("read part: %w", err)
		}

		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			contentType, _, _ := h.ContentType()
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil && !truncated {
				return "", nil, nil, fmt.Errorf("read inline part: %w", readErr)
			}
			switch {
			case isCalendarType(contentType):
				calendars = append(calendars, string(data))
			case isInlineImage(contentType) && readErr == nil:
				// An image embedded in the body (the CID way: no disposition,
				// or an explicit `inline`) used to be read and discarded, so
				// the client could never download it. Count it as an
				// attachment row like any paperclip file. A truncated read
				// yields no row: its size would be wrong and nothing usable
				// follows the cut.
				name := inlineFilename(h.ContentType, h.ContentDisposition)
				if name == "" {
					name = inlineImageName(contentType, len(attachments)+1)
				}
				attachments = append(attachments, core.Attachment{
					Name:   name,
					MIME:   contentType,
					Size:   int64(len(data)),
					Ref:    fmt.Sprintf("part-%d", len(attachments)+1),
					Inline: true,
				})
			case strings.EqualFold(contentType, "text/plain") && !haveText:
				plainText = string(data)
				haveText = true
			case strings.EqualFold(contentType, "text/html") && htmlText == "":
				htmlText = string(data)
			}
			if readErr != nil {
				stop = true // truncated: nothing readable follows the cut
			}
		case *gomail.AttachmentHeader:
			contentType, _, _ := h.ContentType()
			filename, _ := h.Filename()
			data, readErr := io.ReadAll(part.Body)
			if readErr != nil {
				if truncated {
					stop = true
					break
				}
				return "", nil, nil, fmt.Errorf("read attachment part: %w", readErr)
			}
			if isCalendarType(contentType) || strings.HasSuffix(strings.ToLower(filename), ".ics") {
				calendars = append(calendars, string(data))
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
		return plainText, attachments, calendars, nil
	}
	if htmlText != "" {
		return HTMLToText(htmlText), attachments, calendars, nil
	}
	return "", attachments, calendars, nil
}
