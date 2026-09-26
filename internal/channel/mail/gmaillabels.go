package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// go-imap v2 (as of the beta this repo pins) does not expose Gmail's
// X-GM-LABELS IMAP extension: building a raw command requires the
// library's internal wire encoder, which is unexported. storeGmailLabels
// therefore speaks just enough raw IMAP itself, over its own connection,
// to run one UID STORE ... X-GM-LABELS command. See the package's
// open_questions entry about upstreaming this once go-imap supports it.

// buildXGMLabelsStoreLine renders a "tag UID STORE uid <op>X-GM-LABELS
// (...)\r\n" command line. op is "+", "-" or "" (replace).
func buildXGMLabelsStoreLine(tag string, uid imap.UID, op string, labels []string) string {
	quoted := make([]string, len(labels))
	for i, l := range labels {
		quoted[i] = quoteIMAPString(l)
	}
	return fmt.Sprintf("%s UID STORE %d %sX-GM-LABELS (%s)\r\n", tag, uid, op, strings.Join(quoted, " "))
}

// quoteIMAPString renders s as an IMAP quoted string (RFC 9051 4.3.1),
// escaping backslashes and double quotes.
func quoteIMAPString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// storeFlagsOpPrefix maps a StoreFlagsOp to the "+"/"-"/"" prefix
// buildXGMLabelsStoreLine expects.
func storeFlagsOpPrefix(op imap.StoreFlagsOp) string {
	switch op {
	case imap.StoreFlagsAdd:
		return "+"
	case imap.StoreFlagsDel:
		return "-"
	default:
		return ""
	}
}

// rawIMAPConn is a minimal, purpose-built IMAP client for the one
// command go-imap v2 can't send: it authenticates with XOAUTH2 and can
// SELECT a mailbox and run one tagged command line, nothing else.
type rawIMAPConn struct {
	conn net.Conn
	r    *bufio.Reader
	tag  int
}

func dialRawIMAPXOAuth2(ctx context.Context, cfg AccountConfig, tokenSource TokenSource, tlsConfig *tls.Config) (*rawIMAPConn, error) {
	addr := fmt.Sprintf("%s:%d", cfg.IMAPHost, cfg.IMAPPort)
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	c := &rawIMAPConn{conn: conn, r: bufio.NewReader(conn)}

	if _, err := c.readLine(); err != nil { // greeting
		conn.Close()
		return nil, fmt.Errorf("read greeting: %w", err)
	}

	token, err := tokenSource.Token(ctx, cfg.Name)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("get xoauth2 token: %w", err)
	}

	tag := c.nextTag()
	ir := "user=" + cfg.Username + "\x01auth=Bearer " + token + "\x01\x01"
	if err := c.writeLine(fmt.Sprintf("%s AUTHENTICATE XOAUTH2 %s\r\n", tag, base64.StdEncoding.EncodeToString([]byte(ir)))); err != nil {
		conn.Close()
		return nil, err
	}
	line, err := c.readReply()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read authenticate response: %w", err)
	}
	if strings.HasPrefix(line, "+") {
		// Server rejected the token and sent a challenge; reply empty to
		// end the exchange, then read the tagged failure.
		if err := c.writeLine("\r\n"); err != nil {
			conn.Close()
			return nil, err
		}
		line, err = c.readReply()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("read authenticate failure: %w", err)
		}
	}
	if err := checkTagged(tag, line); err != nil {
		conn.Close()
		return nil, fmt.Errorf("authenticate: %w", err)
	}

	return c, nil
}

func (c *rawIMAPConn) Close() error {
	c.writeLine(fmt.Sprintf("%s LOGOUT\r\n", c.nextTag()))
	return c.conn.Close()
}

func (c *rawIMAPConn) nextTag() string {
	c.tag++
	return fmt.Sprintf("R%d", c.tag)
}

func (c *rawIMAPConn) writeLine(line string) error {
	_, err := c.conn.Write([]byte(line))
	return err
}

func (c *rawIMAPConn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// readReply reads the next tagged or continuation ("+") line, skipping
// untagged ("* ...") responses such as the CAPABILITY Gmail sends right
// after a successful AUTHENTICATE.
func (c *rawIMAPConn) readReply() (string, error) {
	for {
		line, err := c.readLine()
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(line, "* ") {
			return line, nil
		}
	}
}

// runTagged writes a command line and reads (and discards) untagged
// responses until it sees the line tagged with this command's tag.
func (c *rawIMAPConn) runTagged(tag, line string) error {
	if err := c.writeLine(line); err != nil {
		return err
	}
	for {
		resp, err := c.readLine()
		if err != nil {
			return err
		}
		if strings.HasPrefix(resp, tag+" ") {
			return checkTagged(tag, resp)
		}
		// untagged data (e.g. "* 1 FETCH (...)"), ignore
	}
}

func (c *rawIMAPConn) Select(mailbox string) error {
	tag := c.nextTag()
	return c.runTagged(tag, fmt.Sprintf("%s SELECT %s\r\n", tag, quoteIMAPString(mailbox)))
}

func (c *rawIMAPConn) StoreGmailLabels(uid imap.UID, op imap.StoreFlagsOp, labels []string) error {
	tag := c.nextTag()
	return c.runTagged(tag, buildXGMLabelsStoreLine(tag, uid, storeFlagsOpPrefix(op), labels))
}

// checkTagged reports whether a tagged response line ("tag OK ...", "tag
// NO ...", "tag BAD ...") indicates success.
func checkTagged(tag, line string) error {
	rest := strings.TrimPrefix(line, tag+" ")
	switch {
	case strings.HasPrefix(rest, "OK"):
		return nil
	default:
		return fmt.Errorf("imap: %s", rest)
	}
}

// storeGmailLabels applies AddLabels/RemoveLabels as X-GM-LABELS over a
// dedicated raw connection (see the package comment above).
func (a *Adapter) storeGmailLabels(ctx context.Context, uid imap.UID, op labelOp) error {
	conn, err := dialRawIMAPXOAuth2(ctx, a.cfg, a.tokenSource, a.gmailTLSConfig)
	if err != nil {
		return fmt.Errorf("gmail labels: %w", err)
	}
	defer conn.Close()

	if err := conn.Select("INBOX"); err != nil {
		return fmt.Errorf("gmail labels: select INBOX: %w", err)
	}
	if len(op.add) > 0 {
		if err := conn.StoreGmailLabels(uid, imap.StoreFlagsAdd, op.add); err != nil {
			return fmt.Errorf("gmail labels: add: %w", err)
		}
	}
	if len(op.remove) > 0 {
		if err := conn.StoreGmailLabels(uid, imap.StoreFlagsDel, op.remove); err != nil {
			return fmt.Errorf("gmail labels: remove: %w", err)
		}
	}
	return nil
}

// labelOp is the add/remove pair storeGmailLabels needs, kept separate
// from core.OrganizeOp so this file has no import-cycle-prone
// dependency beyond what it already has.
type labelOp struct {
	add    []string
	remove []string
}

// fetchGmailLabelsRaw reads X-GM-LABELS for uids over a dedicated raw
// connection (see the package comment above): go-imap v2 can't FETCH
// this Gmail extension item any more than it can STORE it. Every call
// site treats a non-nil error as non-fatal to its own sync/reconcile/
// fetch: log it and leave whatever Labels the item already had, so a
// transient Gmail/network problem here never breaks mail sync.
func (a *Adapter) fetchGmailLabelsRaw(ctx context.Context, uids []imap.UID) (map[imap.UID][]string, error) {
	if len(uids) == 0 {
		return map[imap.UID][]string{}, nil
	}
	conn, err := dialRawIMAPXOAuth2(ctx, a.cfg, a.tokenSource, a.gmailTLSConfig)
	if err != nil {
		return nil, fmt.Errorf("gmail labels fetch: %w", err)
	}
	defer conn.Close()

	if err := conn.Select("INBOX"); err != nil {
		return nil, fmt.Errorf("gmail labels fetch: select INBOX: %w", err)
	}
	labels, err := conn.FetchGmailLabels(uids)
	if err != nil {
		return nil, fmt.Errorf("gmail labels fetch: %w", err)
	}
	return labels, nil
}

// FetchGmailLabels reads X-GM-LABELS for uids over c's raw connection,
// via one "UID FETCH <uids> (X-GM-LABELS)" command, and parses every
// untagged FETCH response line it gets back (parseFetchLabelsLine).
// UIDs the server reports with no X-GM-LABELS data item (unexpected for
// a well-formed request, but handled defensively) are simply absent
// from the returned map.
func (c *rawIMAPConn) FetchGmailLabels(uids []imap.UID) (map[imap.UID][]string, error) {
	tag := c.nextTag()
	var uidList strings.Builder
	for i, uid := range uids {
		if i > 0 {
			uidList.WriteByte(',')
		}
		uidList.WriteString(strconv.FormatUint(uint64(uid), 10))
	}
	line := fmt.Sprintf("%s UID FETCH %s (X-GM-LABELS)\r\n", tag, uidList.String())
	if err := c.writeLine(line); err != nil {
		return nil, err
	}

	out := make(map[imap.UID][]string)
	for {
		resp, err := c.readLine()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(resp, tag+" ") {
			if err := checkTagged(tag, resp); err != nil {
				return nil, err
			}
			return out, nil
		}
		if uid, labels, ok := parseFetchLabelsLine(resp); ok {
			out[uid] = labels
		}
	}
}

// extractUID pulls the "UID <n>" data item's value out of an untagged
// IMAP response line (e.g. "* 3 FETCH (UID 1290 X-GM-LABELS (...))").
func extractUID(line string) (imap.UID, bool) {
	idx := strings.Index(line, "UID ")
	if idx == -1 {
		return 0, false
	}
	rest := line[idx+len("UID "):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(rest[:end], 10, 32)
	if err != nil {
		return 0, false
	}
	return imap.UID(n), true
}

// matchParens returns the content strictly between the first '(' at the
// start of s and its matching ')', honoring double-quoted strings (with
// backslash escapes) so a ')' or another '(' inside a quoted label name
// never unbalances the count.
func matchParens(s string) (string, bool) {
	if len(s) == 0 || s[0] != '(' {
		return "", false
	}
	depth := 0
	inQuote := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQuote {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inQuote = false
			}
			continue
		}
		switch c {
		case '"':
			inQuote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[1:i], true
			}
		}
	}
	return "", false
}

// gmailDroppedSystemLabels lists the Gmail backslash system labels (as
// IMAP sends them, lowercased and without the backslash) that
// parseIMAPLabelList drops entirely instead of keeping as a readable
// name. \Inbox is the only one today: every INBOX item already carries
// that fact via its own folder/mailbox, so keeping it as a Label would
// just be noise. Every other system label (\Sent, \Important, \Trash,
// \Draft, \Spam, \Starred, ...) keeps its name with the backslash
// stripped.
var gmailDroppedSystemLabels = map[string]bool{
	"inbox": true,
}

// parseIMAPLabelList tokenizes a parenthesized IMAP list's contents
// (double-quoted strings with backslash escapes, and bare atoms,
// whitespace-separated) into label strings. A Gmail backslash system
// label (\Sent, \Important, \Trash, \Draft, \Spam, \Starred, ...) keeps
// its name with the backslash stripped; \Inbox is dropped entirely
// (see gmailDroppedSystemLabels).
func parseIMAPLabelList(s string) []string {
	var labels []string
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		var tok string
		if s[i] == '"' {
			j := i + 1
			var sb strings.Builder
			for j < len(s) {
				c := s[j]
				if c == '\\' && j+1 < len(s) {
					sb.WriteByte(s[j+1])
					j += 2
					continue
				}
				if c == '"' {
					j++
					break
				}
				sb.WriteByte(c)
				j++
			}
			tok = sb.String()
			i = j
		} else {
			j := i
			for j < len(s) && s[j] != ' ' {
				j++
			}
			tok = s[i:j]
			i = j
		}
		if tok == "" {
			continue
		}
		if strings.HasPrefix(tok, "\\") {
			name := strings.TrimPrefix(tok, "\\")
			if gmailDroppedSystemLabels[strings.ToLower(name)] {
				continue
			}
			tok = name
		}
		labels = append(labels, tok)
	}
	return labels
}

// parseFetchLabelsLine parses one untagged FETCH response line for its
// UID and X-GM-LABELS data item. ok is false when the line isn't a
// FETCH response with a UID, or has no X-GM-LABELS item at all (e.g. a
// plain FLAGS-only push) — as opposed to an X-GM-LABELS item that is
// simply an empty list, which reports (uid, nil, true).
func parseFetchLabelsLine(line string) (imap.UID, []string, bool) {
	if !strings.Contains(line, "FETCH") {
		return 0, nil, false
	}
	uid, ok := extractUID(line)
	if !ok {
		return 0, nil, false
	}
	idx := strings.Index(line, "X-GM-LABELS")
	if idx == -1 {
		return 0, nil, false
	}
	rest := line[idx+len("X-GM-LABELS"):]
	open := strings.IndexByte(rest, '(')
	if open == -1 {
		return 0, nil, false
	}
	content, ok := matchParens(rest[open:])
	if !ok {
		return 0, nil, false
	}
	return uid, parseIMAPLabelList(content), true
}
