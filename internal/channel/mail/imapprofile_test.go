package mail

import (
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Stock imapmemserver hard-codes '/' as its hierarchy delimiter and has
// no way to give a mailbox special-use attributes, so on its own it can
// only look like one kind of server. The helpers in this file keep it as
// the message store (SELECT, FETCH, APPEND, MOVE, IDLE all go to it
// unchanged) and replace only what LIST reports, so a test can face the
// adapter with the two server styles bunker-go meets in practice:
//
//   - Gmail-like: '/' delimiter, no INBOX prefix, a \Noselect "[Gmail]"
//     parent and every system folder found only through its attribute.
//   - Dovecot-like (generic IMAP/webmail): '.' delimiter, everything under
//     an "INBOX." prefix, no \All, some folders without any attribute,
//     and nested user folders.
//
// The memory backend treats a mailbox name as an opaque key, so
// "INBOX.Clients.Acme" is stored and selected exactly as named; only
// LIST pattern matching and the reported delimiter need the profile.
//
// Limitation worth knowing for Gmail-style tests: a message lives in
// exactly one mailbox here. Real Gmail also shows every message in
// "[Gmail]/All Mail" (it is a label view), and the fake does not.

// profileMailbox is one mailbox a profile's LIST reports.
type profileMailbox struct {
	Name string
	// Attrs are the attributes LIST reports besides the computed
	// \HasChildren/\HasNoChildren, e.g. imap.MailboxAttrSent. A mailbox
	// carrying imap.MailboxAttrNoSelect is listed but never created in
	// the store, so SELECT and MOVE to it fail like on a real server.
	Attrs []imap.MailboxAttr
}

// imapProfile describes how a fake server presents its mailboxes.
type imapProfile struct {
	Name string
	// Delim is the hierarchy delimiter every LIST response carries.
	Delim rune
	// Prefix is the personal namespace prefix ("INBOX" on Dovecot-style
	// servers, "" on Gmail), which is what AccountConfig.FolderPrefix
	// must be set to for this server.
	Prefix    string
	Mailboxes []profileMailbox
}

// gmailProfile is a Gmail-like server: '/' delimiter, no prefix, system
// folders under a \Noselect "[Gmail]" parent and localized-looking names
// ("Sent Mail", "Spam") that only their special-use attribute identifies.
func gmailProfile() imapProfile {
	return imapProfile{
		Name:  "gmail",
		Delim: '/',
		Mailboxes: []profileMailbox{
			{Name: "INBOX"},
			{Name: "[Gmail]", Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}},
			{Name: "[Gmail]/All Mail", Attrs: []imap.MailboxAttr{imap.MailboxAttrAll}},
			{Name: "[Gmail]/Sent Mail", Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}},
			{Name: "[Gmail]/Trash", Attrs: []imap.MailboxAttr{imap.MailboxAttrTrash}},
			{Name: "[Gmail]/Spam", Attrs: []imap.MailboxAttr{imap.MailboxAttrJunk}},
			{Name: "[Gmail]/Drafts", Attrs: []imap.MailboxAttr{imap.MailboxAttrDrafts}},
		},
	}
}

// dovecotOptions varies the Dovecot-like profile.
type dovecotOptions struct {
	// ArchiveAttr advertises \Archive on INBOX.Archive. Webmail installs
	// differ here, and Resolve must find the archive either way.
	ArchiveAttr bool
}

// dovecotProfile is a Dovecot-like (generic IMAP/webmail) server: '.'
// delimiter, everything under the "INBOX." prefix, no \All, Junk and
// Drafts without attributes (as many installs leave them), and nested
// user folders.
func dovecotProfile(opts dovecotOptions) imapProfile {
	var archiveAttrs []imap.MailboxAttr
	if opts.ArchiveAttr {
		archiveAttrs = []imap.MailboxAttr{imap.MailboxAttrArchive}
	}
	return imapProfile{
		Name:   "dovecot",
		Delim:  '.',
		Prefix: "INBOX",
		Mailboxes: []profileMailbox{
			{Name: "INBOX"},
			{Name: "INBOX.Sent", Attrs: []imap.MailboxAttr{imap.MailboxAttrSent}},
			{Name: "INBOX.Trash", Attrs: []imap.MailboxAttr{imap.MailboxAttrTrash}},
			{Name: "INBOX.Junk"},
			{Name: "INBOX.Drafts"},
			{Name: "INBOX.Archive", Attrs: archiveAttrs},
			{Name: "INBOX.Clients"},
			{Name: "INBOX.Clients.Acme"},
		},
	}
}

// profileServer is a running fake IMAP server presenting a profile.
type profileServer struct {
	Addr    string
	Profile imapProfile
	// Mem is the backing store, for tests that need to reach past IMAP.
	// Mailboxes created on it directly are not listed; use
	// CreateMailbox instead.
	Mem *imapmemserver.User

	reg *mailboxRegistry
}

// newProfileIMAPServer starts an in-process, loopback-only fake IMAP
// server presenting p, with every selectable mailbox of p created and
// empty. It accepts testIMAPUsername/testIMAPPassword, so
// testDialInsecure(s.Addr) dials it, and it is closed via t.Cleanup.
func newProfileIMAPServer(t *testing.T, p imapProfile) *profileServer {
	t.Helper()

	memServer := imapmemserver.New()
	user := imapmemserver.NewUser(testIMAPUsername, testIMAPPassword)
	memServer.AddUser(user)

	reg := &mailboxRegistry{delim: p.Delim, attrs: make(map[string][]imap.MailboxAttr)}
	s := &profileServer{Profile: p, Mem: user, reg: reg}
	for _, mb := range p.Mailboxes {
		s.CreateMailbox(t, mb.Name, mb.Attrs...)
	}

	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			inner := pollingIdleSession{memServer.NewSession().(idleTestSession)}
			return profileSession{pollingIdleSession: inner, reg: reg}, nil, nil
		},
		InsecureAuth: true,
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIdle:      {},
			imap.CapMove:      {},
			imap.CapUIDPlus:   {},
			// Both Gmail and Dovecot advertise it, and the attributes
			// below are what it promises.
			imap.CapSpecialUse: {},
		},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })

	s.Addr = ln.Addr().String()
	return s
}

// Config returns an AccountConfig for this server whose FolderPrefix
// and FolderSeparator match the profile, as a user would configure it.
// It leaves Gmail false: the fake has no X-GM-EXT-1, so the Gmail label
// paths would only exercise their failure handling.
func (s *profileServer) Config(name string) AccountConfig {
	return AccountConfig{
		Name:            name,
		IMAPHost:        "unused",
		Username:        testIMAPUsername + "@example.com",
		FolderPrefix:    s.Profile.Prefix,
		FolderSeparator: byte(s.Profile.Delim),
	}
}

// CreateMailbox adds a mailbox to the server after start (e.g. a folder
// a user makes in webmail). A \Noselect mailbox is only listed.
func (s *profileServer) CreateMailbox(t *testing.T, name string, attrs ...imap.MailboxAttr) {
	t.Helper()
	if !hasAttr(attrs, imap.MailboxAttrNoSelect) {
		if err := s.Mem.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	s.reg.add(name, attrs)
}

// seedMessage is a message to put into a mailbox. Zero fields get
// neutral defaults, so a test sets only what it asserts on.
type seedMessage struct {
	From, To, Subject, Body string
	MessageID               string // with angle brackets, e.g. "<a@example.com>"
	References              string
	Date                    time.Time
	Flags                   []imap.Flag
}

// seededMessage is where a seeded message landed.
type seededMessage struct {
	Mailbox     string
	UIDValidity uint32
	UID         imap.UID
	MessageID   string
}

// Seed APPENDs m to mailbox over a separate connection, as a delivery or
// another client would, and reports its UID (the fake supports UIDPLUS).
func (s *profileServer) Seed(t *testing.T, mailbox string, m seedMessage) seededMessage {
	t.Helper()
	if m.From == "" {
		m.From = "Sender <sender@example.com>"
	}
	if m.To == "" {
		m.To = testIMAPUsername + "@example.com"
	}
	if m.Subject == "" {
		m.Subject = "Prueba"
	}
	if m.MessageID == "" {
		m.MessageID = fmt.Sprintf("<seed-%d@example.com>", time.Now().UnixNano())
	}
	if m.Date.IsZero() {
		m.Date = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	}

	var raw strings.Builder
	fmt.Fprintf(&raw, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-Id: %s\r\n", m.From, m.To, m.Subject, m.MessageID)
	if m.References != "" {
		fmt.Fprintf(&raw, "References: %s\r\n", m.References)
	}
	fmt.Fprintf(&raw, "Date: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", m.Date.Format(time.RFC1123Z), m.Body)

	client := s.login(t)
	defer client.Close()
	cmd := client.Append(mailbox, int64(raw.Len()), &imap.AppendOptions{Flags: m.Flags, Time: m.Date})
	if _, err := io.WriteString(cmd, raw.String()); err != nil {
		t.Fatalf("seed %s: write: %v", mailbox, err)
	}
	if err := cmd.Close(); err != nil {
		t.Fatalf("seed %s: close: %v", mailbox, err)
	}
	data, err := cmd.Wait()
	if err != nil {
		t.Fatalf("seed %s: %v", mailbox, err)
	}
	return seededMessage{Mailbox: mailbox, UIDValidity: data.UIDValidity, UID: data.UID, MessageID: m.MessageID}
}

// movedMessage records a move done behind the adapter's back: where the
// message was and where it is now, so a test can check that bunker-go
// follows it instead of treating it as deleted.
type movedMessage struct {
	MessageID         string
	From, To          string // mailbox names
	SourceUIDValidity uint32
	SourceUID         imap.UID
	DestUIDValidity   uint32
	DestUID           imap.UID
}

// MoveAsWebmail moves the message with messageID from one mailbox to
// another over its own connection, the way a webmail or phone client
// does while the adapter is running. The adapter's connection sees only
// an EXPUNGE in the source mailbox.
func (s *profileServer) MoveAsWebmail(t *testing.T, from, to, messageID string) movedMessage {
	t.Helper()
	client := s.login(t)
	defer client.Close()

	src, err := client.Select(from, nil).Wait()
	if err != nil {
		t.Fatalf("webmail move: select %s: %v", from, err)
	}
	uid := findUIDByMessageID(t, client, from, messageID)
	data, err := client.Move(imap.UIDSetNum(uid), to).Wait()
	if err != nil {
		t.Fatalf("webmail move %s from %s to %s: %v", messageID, from, to, err)
	}
	destUID, ok := singleDestUID(data)
	if !ok {
		t.Fatalf("webmail move %s: server reported no COPYUID", messageID)
	}
	return movedMessage{
		MessageID:         messageID,
		From:              from,
		To:                to,
		SourceUIDValidity: src.UIDValidity,
		SourceUID:         uid,
		DestUIDValidity:   data.UIDValidity,
		DestUID:           destUID,
	}
}

// fetchedMessage is what FetchMessage read back.
type fetchedMessage struct {
	UIDValidity uint32
	UID         imap.UID
	Envelope    *imap.Envelope
	Flags       []imap.Flag
	Raw         string
}

// FetchMessage SELECTs mailbox (read-only) on its own connection and
// fetches the message with messageID, failing the test if it is absent.
func (s *profileServer) FetchMessage(t *testing.T, mailbox, messageID string) fetchedMessage {
	t.Helper()
	client := s.login(t)
	defer client.Close()

	mbox, err := client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		t.Fatalf("fetch: select %s: %v", mailbox, err)
	}
	uid := findUIDByMessageID(t, client, mailbox, messageID)
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true,
		BodySection: []*imap.FetchItemBodySection{section},
	}).Collect()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("fetch %s in %s: %d messages, err %v", messageID, mailbox, len(msgs), err)
	}
	return fetchedMessage{
		UIDValidity: mbox.UIDValidity,
		UID:         msgs[0].UID,
		Envelope:    msgs[0].Envelope,
		Flags:       msgs[0].Flags,
		Raw:         string(msgs[0].FindBodySection(section)),
	}
}

// MessageIDs lists the Message-IDs currently in mailbox, sorted, so a
// test can assert where a message is (and is not) after a move.
func (s *profileServer) MessageIDs(t *testing.T, mailbox string) []string {
	t.Helper()
	client := s.login(t)
	defer client.Close()
	if _, err := client.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		t.Fatalf("list ids: select %s: %v", mailbox, err)
	}
	var ids []string
	for _, msg := range fetchAllEnvelopes(t, client, mailbox) {
		ids = append(ids, msg.Envelope.MessageID)
	}
	sort.Strings(ids)
	return ids
}

func (s *profileServer) login(t *testing.T) *imapclient.Client {
	t.Helper()
	conn, err := net.Dial("tcp", s.Addr)
	if err != nil {
		t.Fatalf("dial fake IMAP: %v", err)
	}
	client := imapclient.New(conn, nil)
	if err := client.Login(testIMAPUsername, testIMAPPassword).Wait(); err != nil {
		client.Close()
		t.Fatalf("login fake IMAP: %v", err)
	}
	return client
}

// findUIDByMessageID matches envelopes exactly; SEARCH HEADER is a
// substring match and could pick a message whose id merely contains it.
func findUIDByMessageID(t *testing.T, client *imapclient.Client, mailbox, messageID string) imap.UID {
	t.Helper()
	// Envelopes carry the Message-ID without angle brackets.
	want := strings.Trim(messageID, "<>")
	for _, msg := range fetchAllEnvelopes(t, client, mailbox) {
		if msg.Envelope.MessageID == want {
			return msg.UID
		}
	}
	t.Fatalf("no message %s in %s", messageID, mailbox)
	return 0
}

func fetchAllEnvelopes(t *testing.T, client *imapclient.Client, mailbox string) []*imapclient.FetchMessageBuffer {
	t.Helper()
	var all imap.SeqSet
	all.AddRange(1, 0)
	msgs, err := client.Fetch(all, &imap.FetchOptions{UID: true, Envelope: true}).Collect()
	if err != nil {
		t.Fatalf("fetch envelopes in %s: %v", mailbox, err)
	}
	for _, msg := range msgs {
		if msg.Envelope == nil {
			t.Fatalf("fetch envelopes in %s: UID %d has none", mailbox, msg.UID)
		}
	}
	return msgs
}

// mailboxRegistry is the profile's view of which mailboxes exist and
// what LIST says about them; it is shared by every session of a server.
type mailboxRegistry struct {
	delim rune

	mu    sync.Mutex
	attrs map[string][]imap.MailboxAttr
}

func (r *mailboxRegistry) add(name string, attrs []imap.MailboxAttr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attrs[name] = append([]imap.MailboxAttr(nil), attrs...)
}

func (r *mailboxRegistry) remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attrs, name)
}

// list builds the LIST responses for ref/patterns, sorted by name.
// Special-use attributes are sent even without RETURN (SPECIAL-USE):
// Gmail and Dovecot both do, and a client must cope with it.
func (r *mailboxRegistry) list(ref string, patterns []string, options *imap.ListOptions) []imap.ListData {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []imap.ListData
	for name, attrs := range r.attrs {
		match := false
		for _, pattern := range patterns {
			if imapserver.MatchList(name, r.delim, ref, pattern) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		if options.SelectSpecialUse && !hasSpecialUse(attrs) {
			continue
		}
		data := imap.ListData{Mailbox: name, Delim: r.delim}
		data.Attrs = append(data.Attrs, attrs...)
		if r.hasChildrenLocked(name) {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasChildren)
		} else {
			data.Attrs = append(data.Attrs, imap.MailboxAttrHasNoChildren)
		}
		out = append(out, data)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mailbox < out[j].Mailbox })
	return out
}

func (r *mailboxRegistry) hasChildrenLocked(name string) bool {
	childPrefix := name + string(r.delim)
	for other := range r.attrs {
		if strings.HasPrefix(other, childPrefix) {
			return true
		}
	}
	return false
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

func hasSpecialUse(attrs []imap.MailboxAttr) bool {
	for _, a := range attrs {
		switch a {
		case imap.MailboxAttrAll, imap.MailboxAttrArchive, imap.MailboxAttrDrafts,
			imap.MailboxAttrFlagged, imap.MailboxAttrJunk, imap.MailboxAttrSent, imap.MailboxAttrTrash:
			return true
		}
	}
	return false
}

// profileSession answers LIST from the profile's registry and keeps it
// in step with CREATE/DELETE/RENAME; everything else goes to the memory
// backend (through pollingIdleSession, so IDLE behaves like Dovecot's).
type profileSession struct {
	pollingIdleSession
	reg *mailboxRegistry
}

func (s profileSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if len(patterns) == 0 {
		// LIST "" "" asks only for the delimiter (RFC 3501 6.3.8).
		return w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: s.reg.delim})
	}
	for _, data := range s.reg.list(ref, patterns, options) {
		if options.ReturnStatus != nil && !hasAttr(data.Attrs, imap.MailboxAttrNoSelect) {
			status, err := s.Status(data.Mailbox, options.ReturnStatus)
			if err != nil {
				return err
			}
			data.Status = status
		}
		if err := w.WriteList(&data); err != nil {
			return err
		}
	}
	return nil
}

func (s profileSession) Create(name string, options *imap.CreateOptions) error {
	name = strings.TrimRight(name, string(s.reg.delim))
	if err := s.pollingIdleSession.Create(name, options); err != nil {
		return err
	}
	var attrs []imap.MailboxAttr
	if options != nil {
		attrs = options.SpecialUse
	}
	s.reg.add(name, attrs)
	return nil
}

func (s profileSession) Delete(name string) error {
	if err := s.pollingIdleSession.Delete(name); err != nil {
		return err
	}
	s.reg.remove(name)
	return nil
}

func (s profileSession) Rename(oldName, newName string, options *imap.RenameOptions) error {
	if err := s.pollingIdleSession.Rename(oldName, newName, options); err != nil {
		return err
	}
	s.reg.mu.Lock()
	attrs := s.reg.attrs[oldName]
	s.reg.mu.Unlock()
	s.reg.remove(oldName)
	s.reg.add(newName, attrs)
	return nil
}
