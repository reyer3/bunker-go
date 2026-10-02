package mail

import (
	"fmt"
	"log"
	"unicode/utf8"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// defaultIndexBodyMaxKB is how much of each message's text sync fetches
// for the full-text index (#91) when an account sets no
// index_body_max_kb: enough for virtually any hand-written mail, small
// enough that a 200-message initial sync stays a few megabytes.
const defaultIndexBodyMaxKB = 64

// indexBodyMaxBytes is the per-message cap on sync's body fetch, 0 when
// the account disabled it.
func (a *Adapter) indexBodyMaxBytes() int {
	if a.cfg.IndexBodyMaxKB <= 0 {
		return 0
	}
	return a.cfg.IndexBodyMaxKB * 1024
}

// addBodyTextSections asks a sync FETCH for what fillBodyText needs: the
// whole header (for Content-Type and the MIME structure it announces)
// and the first indexBodyMaxBytes of the text after it. Both are PEEK,
// so syncing never sets \Seen, and the TEXT range is partial, so a big
// attachment is never downloaded whole just to index the words before
// it. The References header section sync already asks for stays as is.
func (a *Adapter) addBodyTextSections(opts *imap.FetchOptions) {
	limit := a.indexBodyMaxBytes()
	if limit == 0 {
		return
	}
	opts.BodySection = append(opts.BodySection,
		&imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true},
		&imap.FetchItemBodySection{
			Specifier: imap.PartSpecifierText,
			Peek:      true,
			Partial:   &imap.SectionPartial{Offset: 0, Size: int64(limit)},
		},
	)
}

// fillBodyText sets item.Body from the sections addBodyTextSections
// requested, leaving it empty when they are absent (the option is off,
// or the server sent nothing). Attachments are left alone: from a cut
// message their sizes would be wrong, and the store keeps the ones a
// full read already saved.
func (a *Adapter) fillBodyText(item *core.Item, msg *imapclient.FetchMessageBuffer) {
	limit := a.indexBodyMaxBytes()
	if limit == 0 {
		return
	}
	var header, text []byte
	for _, section := range msg.BodySection {
		if section.Section == nil || len(section.Section.Part) > 0 {
			continue
		}
		// Matched by specifier rather than FindBodySection's exact match:
		// a server may leave the "<0>" origin off a partial response.
		switch {
		case section.Section.Specifier == imap.PartSpecifierHeader && len(section.Section.HeaderFields) == 0:
			header = section.Bytes
		case section.Section.Specifier == imap.PartSpecifierText:
			text = section.Bytes
		}
	}
	if header == nil || len(text) == 0 {
		return
	}
	body, calendars, err := bodyTextFromPartial(header, text, limit)
	if err != nil {
		// Indexing is best-effort: the message still syncs, and a read
		// fetches and parses it whole, reporting any error then.
		log.Printf("mail: sync: index body of %s: %v", item.ID, err)
		return
	}
	item.Body = body
	setMeeting(item, calendars)
}

// bodyTextFromPartial rebuilds a (possibly cut) message from its header
// and the start of its text, extracts the readable text with the same
// parser a full read uses (text/plain preferred, HTML rendered to text),
// and caps it at limit bytes. A text shorter than limit is the whole
// text, so only a range that may have been cut is parsed tolerantly.
func bodyTextFromPartial(header, text []byte, limit int) (string, []string, error) {
	raw := make([]byte, 0, len(header)+len(text))
	raw = append(raw, header...)
	raw = append(raw, text...)
	body, _, calendars, err := walkMessage(raw, len(text) >= limit)
	if err != nil {
		return "", nil, fmt.Errorf("parse body: %w", err)
	}
	return truncateUTF8(body, limit), calendars, nil
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune, and
// drops an incomplete rune the byte range fetch itself may have cut at
// the end (an undecoded 8bit body cut mid-character): the store and the
// FTS tokenizer should only ever see valid UTF-8 at the tail.
func truncateUTF8(s string, limit int) string {
	if len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	for i := 0; i < utf8.UTFMax-1 && s != ""; i++ {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
