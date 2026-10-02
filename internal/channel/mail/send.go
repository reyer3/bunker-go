package mail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// smtpDialFunc opens an authenticated SMTP submission connection for
// cfg. Tests substitute a plaintext dial against an in-process go-smtp
// server for the real STARTTLS/implicit-TLS dial (smtpDialReal).
type smtpDialFunc func(ctx context.Context, cfg AccountConfig, passwordSource PasswordSource, tokenSource TokenSource) (*smtp.Client, error)

// smtpDialReal is the production smtpDialFunc: implicit TLS on port 465,
// STARTTLS otherwise, then AUTH PLAIN or AUTH XOAUTH2.
func smtpDialReal(ctx context.Context, cfg AccountConfig, passwordSource PasswordSource, tokenSource TokenSource) (*smtp.Client, error) {
	addr := fmt.Sprintf("%s:%d", cfg.SMTPHost, cfg.SMTPPort)

	var client *smtp.Client
	var err error
	if cfg.SMTPPort == 465 {
		client, err = smtp.DialTLS(addr, nil)
	} else {
		client, err = smtp.DialStartTLS(addr, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("mail: smtp dial %s: %w", addr, err)
	}

	var authClient sasl.Client
	switch cfg.Auth {
	case AuthXOAuth2:
		token, err := tokenSource.Token(ctx, cfg.Name)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: smtp get xoauth2 token for %q: %w", cfg.Name, err)
		}
		authClient = newXOAuth2Client(cfg.Username, token)
	default:
		password, err := passwordSource.Password(ctx, cfg.Name)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("mail: smtp get password for %q: %w", cfg.Name, err)
		}
		authClient = sasl.NewPlainClient("", cfg.Username, password)
	}
	if err := client.Auth(authClient); err != nil {
		client.Close()
		return nil, fmt.Errorf("mail: smtp auth %q: %w", cfg.Name, err)
	}

	return client, nil
}

// Send implements core.Sender: it submits out over SMTP, appends the
// sent message to the account's Sent mailbox (skipped for Gmail, which
// auto-saves sent mail), and, for a reply, sets \Answered on the
// original message.
func (a *Adapter) Send(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	imapClient, err := a.dial(ctx, a.cfg, a.passwordSource, a.tokenSource, nil)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: %w", err)
	}
	defer imapClient.Close()

	folders, err := discoverFolders(ctx, imapClient, a.cfg)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: %w", err)
	}

	subject := out.Subject
	var inReplyTo string
	var references []string
	var origUID imap.UID
	var origMailbox string
	isReply := out.ReplyTo != ""

	if isReply {
		account, origFolder, uidValidity, uid, err := parseItemID(out.ReplyTo)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: reply to %q: %w", out.ReplyTo, err)
		}
		if account != a.cfg.Name {
			return core.Receipt{}, fmt.Errorf("mail: send: reply target %q is not for account %q: %w", out.ReplyTo, a.cfg.Name, core.ErrNotFound)
		}
		origUID = uid
		// #52: the original may live in any synced folder, not just INBOX.
		origMailbox = mailboxFor(folders, origFolder)

		mbox, err := imapClient.Select(origMailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: select %s: %w", origMailbox, err)
		}
		if mbox.UIDValidity != uidValidity {
			return core.Receipt{}, fmt.Errorf("mail: send: reply target %q: mailbox UIDVALIDITY changed: %w", out.ReplyTo, core.ErrNotFound)
		}

		origSubject, origMessageID, origReferences, err := fetchEnvelopeAndReferences(imapClient, uid)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: fetch original %q: %w", out.ReplyTo, err)
		}
		if subject == "" {
			subject = ReplySubject(origSubject)
		}
		inReplyTo = origMessageID
		references = ReplyReferences(origMessageID, origReferences)
	}

	messageID, err := newMessageID(messageIDDomain(a.cfg.Username, a.cfg.IMAPHost))
	if err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: generate message id: %w", err)
	}

	raw, err := buildRawMessage(a.cfg, out, subject, messageID, inReplyTo, references, a.now())
	if err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: build message: %w", err)
	}

	smtpClient, err := a.smtpDial(ctx, a.cfg, a.passwordSource, a.tokenSource)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: %w", err)
	}
	defer smtpClient.Close()

	recipients := append(append([]string{}, out.To...), out.Cc...)
	if err := smtpClient.SendMail(a.cfg.Username, recipients, bytes.NewReader(raw)); err != nil {
		return core.Receipt{}, fmt.Errorf("mail: send: smtp submit: %w", err)
	}

	if !a.cfg.Gmail {
		sentMailbox := folders.Resolve("Sent")
		appendCmd := imapClient.Append(sentMailbox, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		if _, err := appendCmd.Write(raw); err != nil {
			appendCmd.Close()
			return core.Receipt{}, fmt.Errorf("mail: send: append to %s: %w", sentMailbox, err)
		}
		if err := appendCmd.Close(); err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: append to %s: %w", sentMailbox, err)
		}
		if _, err := appendCmd.Wait(); err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: append to %s: %w", sentMailbox, err)
		}
	}

	if isReply {
		if _, err := imapClient.Select(origMailbox, nil).Wait(); err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: re-select %s to mark answered: %w", origMailbox, err)
		}
		storeFlags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagAnswered}}
		if err := imapClient.Store(imap.UIDSetNum(origUID), storeFlags, nil).Close(); err != nil {
			return core.Receipt{}, fmt.Errorf("mail: send: mark \\Answered: %w", err)
		}
	}

	return core.Receipt{ID: messageID, Channel: core.ChannelMail, At: a.now()}, nil
}

// fetchEnvelopeAndReferences reads just enough of a message to build a
// reply: its Subject, Message-ID and References chain. It always uses
// BODY.PEEK, so looking up the original never marks it \Seen.
func fetchEnvelopeAndReferences(client *imapclient.Client, uid imap.UID) (subject, messageID string, references []string, err error) {
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{{
			Specifier:    imap.PartSpecifierHeader,
			HeaderFields: []string{"References"},
			Peek:         true,
		}},
	}
	messages, err := client.Fetch(imap.UIDSetNum(uid), fetchOptions).Collect()
	if err != nil {
		return "", "", nil, fmt.Errorf("fetch: %w", err)
	}
	if len(messages) == 0 {
		return "", "", nil, core.ErrNotFound
	}
	msg := messages[0]
	if msg.Envelope != nil {
		subject = msg.Envelope.Subject
		messageID = msg.Envelope.MessageID
	}
	for _, section := range msg.BodySection {
		references = parseReferences(section.Bytes)
	}
	return subject, messageID, references, nil
}

// messageIDDomain returns the domain a generated Message-ID should use:
// the sender's own address domain (the part of username after '@'),
// which is what recipients and receiving MTAs expect. It falls back to
// host — the IMAP host, the previous (buggy) behavior — only when
// username carries no '@', so a misconfigured account still gets a
// Message-ID instead of an error.
func messageIDDomain(username, host string) string {
	if _, domain, ok := strings.Cut(username, "@"); ok && domain != "" {
		return domain
	}
	return host
}

func newMessageID(host string) (string, error) {
	var h gomail.Header
	if host == "" {
		host = "bunker-go.local"
	}
	if err := h.GenerateMessageIDWithHostname(host); err != nil {
		return "", err
	}
	return h.MessageID()
}

// buildRawMessage renders out as an RFC 5322 message ready for SMTP
// submission and IMAP APPEND.
func buildRawMessage(cfg AccountConfig, out core.Outgoing, subject, messageID, inReplyTo string, references []string, at time.Time) ([]byte, error) {
	var h gomail.Header
	h.SetAddressList("From", []*gomail.Address{{Address: cfg.Username}})
	to := make([]*gomail.Address, 0, len(out.To))
	for _, addr := range out.To {
		to = append(to, &gomail.Address{Address: addr})
	}
	h.SetAddressList("To", to)
	if len(out.Cc) > 0 {
		cc := make([]*gomail.Address, 0, len(out.Cc))
		for _, addr := range out.Cc {
			cc = append(cc, &gomail.Address{Address: addr})
		}
		h.SetAddressList("Cc", cc)
	}
	h.SetSubject(subject)
	h.SetDate(at)
	h.SetMessageID(messageID)
	if inReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{inReplyTo})
	}
	if len(references) > 0 {
		h.SetMsgIDList("References", references)
	}

	var buf bytes.Buffer
	if len(out.Attachments) == 0 {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w, err := gomail.CreateSingleInlineWriter(&buf, h)
		if err != nil {
			return nil, fmt.Errorf("create writer: %w", err)
		}
		if _, err := w.Write([]byte(out.Body)); err != nil {
			return nil, fmt.Errorf("write body: %w", err)
		}
		if err := w.Close(); err != nil {
			return nil, fmt.Errorf("close writer: %w", err)
		}
		return buf.Bytes(), nil
	}

	mw, err := gomail.CreateWriter(&buf, h)
	if err != nil {
		return nil, fmt.Errorf("create writer: %w", err)
	}

	var textHeader gomail.InlineHeader
	textHeader.Set("Content-Type", "text/plain; charset=utf-8")
	tw, err := mw.CreateSingleInline(textHeader)
	if err != nil {
		return nil, fmt.Errorf("create text part: %w", err)
	}
	if _, err := tw.Write([]byte(out.Body)); err != nil {
		return nil, fmt.Errorf("write body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close text part: %w", err)
	}

	for _, path := range out.Attachments {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read attachment %s: %w", path, err)
		}
		contentType := core.AttachmentMIME(path, data)
		var attHeader gomail.AttachmentHeader
		attHeader.Set("Content-Type", contentType)
		attHeader.SetFilename(filepath.Base(path))
		aw, err := mw.CreateAttachment(attHeader)
		if err != nil {
			return nil, fmt.Errorf("create attachment %s: %w", path, err)
		}
		if _, err := aw.Write(data); err != nil {
			return nil, fmt.Errorf("write attachment %s: %w", path, err)
		}
		if err := aw.Close(); err != nil {
			return nil, fmt.Errorf("close attachment %s: %w", path, err)
		}
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}
	return buf.Bytes(), nil
}
