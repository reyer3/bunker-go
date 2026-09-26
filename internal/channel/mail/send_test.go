package mail

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

const (
	testSMTPUsername = "alice"
	testSMTPPassword = "s3cr3t"
)

// capturingSMTPBackend is an in-process go-smtp backend that requires
// AUTH PLAIN with fixed test credentials and records every submitted
// message, so Send tests never touch a real mail server.
type capturingSMTPBackend struct {
	mu   sync.Mutex
	subs []capturedSubmission
}

type capturedSubmission struct {
	from string
	to   []string
	data []byte
}

func (b *capturingSMTPBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &capturingSMTPSession{backend: b}, nil
}

func (b *capturingSMTPBackend) submissions() []capturedSubmission {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]capturedSubmission, len(b.subs))
	copy(out, b.subs)
	return out
}

type capturingSMTPSession struct {
	backend *capturingSMTPBackend
	authed  bool
	from    string
	to      []string
}

func (s *capturingSMTPSession) AuthMechanisms() []string { return []string{sasl.Plain, "XOAUTH2"} }

func (s *capturingSMTPSession) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(identity, username, password string) error {
			if username != testSMTPUsername || password != testSMTPPassword {
				return errors.New("bad credentials")
			}
			s.authed = true
			return nil
		}), nil
	default:
		return nil, errors.New("unsupported mechanism in test backend")
	}
}

func (s *capturingSMTPSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authed {
		return smtp.ErrAuthRequired
	}
	s.from = from
	return nil
}

func (s *capturingSMTPSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *capturingSMTPSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.backend.mu.Lock()
	s.backend.subs = append(s.backend.subs, capturedSubmission{from: s.from, to: append([]string{}, s.to...), data: data})
	s.backend.mu.Unlock()
	return nil
}

func (s *capturingSMTPSession) Reset()        {}
func (s *capturingSMTPSession) Logout() error { return nil }

func newTestSMTPServer(t *testing.T) (addr string, backend *capturingSMTPBackend) {
	t.Helper()
	backend = &capturingSMTPBackend{}
	server := smtp.NewServer(backend)
	server.AllowInsecureAuth = true
	server.Domain = "localhost"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	return ln.Addr().String(), backend
}

func testSMTPDialInsecure(addr string) smtpDialFunc {
	return func(ctx context.Context, cfg AccountConfig, _ PasswordSource, _ TokenSource) (*smtp.Client, error) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		client := smtp.NewClient(conn)
		if err := client.Auth(sasl.NewPlainClient("", testSMTPUsername, testSMTPPassword)); err != nil {
			client.Close()
			return nil, err
		}
		return client, nil
	}
}

func TestAdapterSendNewMail(t *testing.T) {
	imapAddr, mem, _ := newMemIMAPServer(t)
	// imapmemserver always reports '/' as its hierarchy separator
	// (imapmemserver.mailboxDelim), regardless of what a config might
	// say; the adapter must use the server's real separator, not the
	// configured one, so the fixture and assertions below use '/' too.
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	smtpAddr, backend := newTestSMTPServer(t)

	cfg := AccountConfig{Name: "cl", Username: "alice@example.cl", IMAPHost: "example.cl", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	out := core.Outgoing{
		Channel: core.ChannelMail,
		Account: "cl",
		To:      []string{"alice@example.org"},
		Subject: "Hello",
		Body:    "Hi Alice",
	}
	receipt, err := adapter.Send(context.Background(), out)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if receipt.ID == "" {
		t.Error("Receipt.ID is empty")
	}

	subs := backend.submissions()
	if len(subs) != 1 {
		t.Fatalf("len(submissions) = %d, want 1", len(subs))
	}
	if subs[0].from != cfg.Username {
		t.Errorf("From = %q, want %q", subs[0].from, cfg.Username)
	}
	if len(subs[0].to) != 1 || subs[0].to[0] != "alice@example.org" {
		t.Errorf("To = %v, want [alice@example.org]", subs[0].to)
	}
	if !strings.Contains(string(subs[0].data), "Hi Alice") {
		t.Errorf("submitted data = %q, want it to contain the body", subs[0].data)
	}

	// The Sent mailbox should now contain the message too (Dovecot: not
	// auto-saved by the server, so the adapter must APPEND it itself).
	verifyMailboxHasMessages(t, imapAddr, "INBOX/Sent", 1)
}

// TestAdapterSendToMultipleRecipientsAndCc covers T12(c): every To and Cc
// address must reach both the SMTP envelope (RCPT TO) and the message
// headers, never just out.To[0].
func TestAdapterSendToMultipleRecipientsAndCc(t *testing.T) {
	imapAddr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	smtpAddr, backend := newTestSMTPServer(t)

	cfg := AccountConfig{Name: "cl", Username: "alice@example.cl", IMAPHost: "example.cl", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	out := core.Outgoing{
		Channel: core.ChannelMail,
		Account: "cl",
		To:      []string{"alice@example.org", "bob@example.org"},
		Cc:      []string{"carol@example.org"},
		Subject: "Carousel",
		Body:    "Hi all",
	}
	if _, err := adapter.Send(context.Background(), out); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	subs := backend.submissions()
	if len(subs) != 1 {
		t.Fatalf("len(submissions) = %d, want 1", len(subs))
	}

	wantRCPT := map[string]bool{"alice@example.org": false, "bob@example.org": false, "carol@example.org": false}
	if len(subs[0].to) != len(wantRCPT) {
		t.Fatalf("RCPT TO = %v, want exactly %v", subs[0].to, wantRCPT)
	}
	for _, addr := range subs[0].to {
		if _, ok := wantRCPT[addr]; !ok {
			t.Fatalf("unexpected RCPT TO %q, full list %v", addr, subs[0].to)
		}
		wantRCPT[addr] = true
	}
	for addr, seen := range wantRCPT {
		if !seen {
			t.Fatalf("RCPT TO missing %q, full list %v", addr, subs[0].to)
		}
	}

	raw := string(subs[0].data)
	if !strings.Contains(raw, "To: <alice@example.org>, <bob@example.org>") {
		t.Errorf("submitted data missing combined To header:\n%s", raw)
	}
	if !strings.Contains(raw, "Cc: <carol@example.org>") {
		t.Errorf("submitted data missing Cc header:\n%s", raw)
	}
}

func TestAdapterSendReplySetsThreadingAndAnswered(t *testing.T) {
	imapAddr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	appendMessage(t, imapAddr, "INBOX", rawMessage(
		"<orig@example.org>", "", "Meet recording",
		"Alice <alice@example.org>", "alice@example.cl", "original body",
	))
	smtpAddr, backend := newTestSMTPServer(t)

	cfg := AccountConfig{Name: "cl", Username: "alice@example.cl", IMAPHost: "example.cl", FolderPrefix: "INBOX"}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	// Learn the seeded message's real item id the way Run would.
	ctx, cancel := context.WithCancel(context.Background())
	sink := newFakeSink()
	done := make(chan error, 1)
	go func() { done <- adapter.Run(ctx, sink) }()
	seed := waitForUpsert(t, sink, 5*time.Second)
	cancel()
	<-done

	out := core.Outgoing{
		Channel: core.ChannelMail,
		Account: "cl",
		To:      []string{"alice@example.org"},
		ReplyTo: seed.ID,
		Body:    "reply body",
	}
	if _, err := adapter.Send(context.Background(), out); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	subs := backend.submissions()
	if len(subs) != 1 {
		t.Fatalf("len(submissions) = %d, want 1", len(subs))
	}
	raw := string(subs[0].data)
	if !strings.Contains(raw, "Subject: Re: Meet recording") {
		t.Errorf("submitted data missing Re: subject:\n%s", raw)
	}
	if !strings.Contains(raw, "In-Reply-To: <orig@example.org>") {
		t.Errorf("submitted data missing In-Reply-To:\n%s", raw)
	}
	if !strings.Contains(raw, "References: <orig@example.org>") {
		t.Errorf("submitted data missing References:\n%s", raw)
	}

	// The original message must now be \Answered.
	verifyMessageFlagged(t, imapAddr, "INBOX", "orig@example.org")
}

func verifyMessageFlagged(t *testing.T, addr, mailbox, messageID string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for verify: %v", err)
	}
	defer conn.Close()
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for verify: %v", err)
	}
	if _, err := client.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var seqSet imap.SeqSet
	seqSet.AddRange(1, 0)
	messages, err := client.Fetch(seqSet, &imap.FetchOptions{Flags: true, Envelope: true}).Collect()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, msg := range messages {
		if msg.Envelope == nil || msg.Envelope.MessageID != messageID {
			continue
		}
		for _, flag := range msg.Flags {
			if flag == imap.FlagAnswered {
				return
			}
		}
		t.Fatalf("message %s flags = %v, want \\Answered among them", messageID, msg.Flags)
	}
	t.Fatalf("message %s not found in %s", messageID, mailbox)
}

func verifyMailboxHasMessages(t *testing.T, addr, mailbox string, want int) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial for verify: %v", err)
	}
	defer conn.Close()
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		t.Fatalf("login for verify: %v", err)
	}
	mbox, err := client.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	if int(mbox.NumMessages) != want {
		t.Errorf("%s has %d messages, want %d", mailbox, mbox.NumMessages, want)
	}
}

func TestAdapterSendGmailSkipsAppendToSent(t *testing.T) {
	// Deliberately no "Sent" mailbox created: Gmail auto-saves sent mail
	// itself, so a Gmail-configured adapter must never try to APPEND to
	// it (which would otherwise fail with TRYCREATE, as the no-mailbox
	// test above shows for a non-Gmail account).
	imapAddr, _, _ := newMemIMAPServer(t)
	smtpAddr, backend := newTestSMTPServer(t)

	cfg := AccountConfig{Name: "com", Username: "alice@example.com", IMAPHost: "gmail.com", Gmail: true}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "com", To: []string{"bob@example.org"}, Subject: "Hi", Body: "hello"}
	if _, err := adapter.Send(context.Background(), out); err != nil {
		t.Fatalf("Send() error = %v, want no APPEND attempt for a Gmail account", err)
	}
	if len(backend.submissions()) != 1 {
		t.Fatalf("len(submissions) = %d, want 1", len(backend.submissions()))
	}
}

// TestMessageIDDomainUsesSenderAddress covers T14(b): the generated
// Message-ID must use the sender's own domain (cfg.Username after '@'),
// not the IMAP host — live bug found 2026-09-25, where sent mail on both
// accounts carried the IMAP hostname (@imap.gmail.com,
// @mail.example.cl) instead of the account's real domain.
func TestMessageIDDomainUsesSenderAddress(t *testing.T) {
	tests := []struct {
		name     string
		username string
		host     string
		want     string
	}{
		{name: "cl account", username: "alice@example.cl", host: "mail.example.cl", want: "example.cl"},
		{name: "com account (gmail)", username: "alice@example.com", host: "imap.gmail.com", want: "example.com"},
		{name: "no @ in username falls back to host", username: "alice", host: "mail.example.cl", want: "mail.example.cl"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageIDDomain(tt.username, tt.host); got != tt.want {
				t.Errorf("messageIDDomain(%q, %q) = %q, want %q", tt.username, tt.host, got, tt.want)
			}
		})
	}
}

// TestAdapterSendMessageIDUsesSenderDomain is the integration-level
// twin of TestMessageIDDomainUsesSenderAddress: a real Send() call's
// receipt ID (the generated Message-ID) must end with the sender's own
// domain even though the configured IMAPHost differs from it.
func TestAdapterSendMessageIDUsesSenderDomain(t *testing.T) {
	imapAddr, mem, _ := newMemIMAPServer(t)
	if err := mem.Create("INBOX/Sent", nil); err != nil {
		t.Fatalf("create INBOX/Sent: %v", err)
	}
	smtpAddr, _ := newTestSMTPServer(t)

	cfg := AccountConfig{Name: "cl", Username: "alice@example.cl", IMAPHost: "mail.example.cl", FolderPrefix: "INBOX", FolderSeparator: '.'}
	adapter := newAdapter(cfg, nil, nil, testDialInsecure(imapAddr))
	adapter.smtpDial = testSMTPDialInsecure(smtpAddr)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"alice@example.org"}, Subject: "Hi", Body: "hello"}
	receipt, err := adapter.Send(context.Background(), out)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !strings.HasSuffix(receipt.ID, "@example.cl") {
		t.Errorf("receipt.ID = %q, want it to end with @example.cl (the sender's domain, not IMAPHost)", receipt.ID)
	}
}
