package mail

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// fakeGmailIMAPServer is a tiny, purpose-built stand-in for Gmail's IMAP
// server: just enough of the wire protocol (greeting, AUTHENTICATE
// XOAUTH2, SELECT, UID STORE ... X-GM-LABELS, LOGOUT) to exercise
// rawIMAPConn end to end, entirely in-process, over a self-signed TLS
// listener.
type fakeGmailIMAPServer struct {
	storedLine chan string
}

func newFakeGmailIMAPServer(t *testing.T) (addr string, srv *fakeGmailIMAPServer) {
	t.Helper()
	srv = &fakeGmailIMAPServer{storedLine: make(chan string, 1)}

	cert := generateSelfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		srv.serve(conn)
	}()

	return ln.Addr().String(), srv
}

func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"bunker-go test"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert
}

func (s *fakeGmailIMAPServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	conn.Write([]byte("* OK fake gmail ready\r\n"))

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		tag := fields[0]
		switch {
		case strings.Contains(line, "AUTHENTICATE XOAUTH2"):
			// Real Gmail sends an untagged CAPABILITY before the tagged OK
			// (observed live 2026-09-25); the client must skip it.
			conn.Write([]byte("* CAPABILITY IMAP4rev1 UNSELECT IDLE NAMESPACE QUOTA ID XLIST CHILDREN X-GM-EXT-1 UIDPLUS COMPRESS=DEFLATE ENABLE MOVE CONDSTORE ESEARCH UTF8=ACCEPT LIST-EXTENDED LIST-STATUS LITERAL- SPECIAL-USE APPENDLIMIT=35651584\r\n"))
			conn.Write([]byte(tag + " OK alice@example.com authenticated (Success)\r\n"))
		case strings.Contains(line, "SELECT"):
			conn.Write([]byte(tag + " OK [READ-WRITE] Select completed\r\n"))
		case strings.Contains(line, "UID FETCH") && strings.Contains(line, "X-GM-LABELS"):
			// Gmail sends one untagged FETCH line per requested UID
			// (observed shape; real IDs/labels vary). Two fixed demo UIDs
			// (100, 101) get a specific canned label set each, for the
			// parser-focused raw-fetch test; any other requested UID
			// (e.g. one a real sync produced) echoes back with a single
			// "bunker-test" label, so an end-to-end test can assert on it
			// without knowing the exact UID in advance.
			for _, uid := range parseRequestedUIDs(line) {
				switch uid {
				case 100:
					conn.Write([]byte("* 1 FETCH (UID 100 X-GM-LABELS (\\Important \"Muy Importante\" bunker-test))\r\n"))
				case 101:
					conn.Write([]byte("* 2 FETCH (UID 101 X-GM-LABELS (\\Sent))\r\n"))
				default:
					conn.Write([]byte(fmt.Sprintf("* 1 FETCH (UID %d X-GM-LABELS (bunker-test))\r\n", uid)))
				}
			}
			conn.Write([]byte(tag + " OK Success\r\n"))
		case strings.Contains(line, "X-GM-LABELS"):
			select {
			case s.storedLine <- line:
			default:
			}
			conn.Write([]byte(tag + " OK Success\r\n"))
		case strings.Contains(line, "LOGOUT"):
			conn.Write([]byte("* BYE logging out\r\n"))
			conn.Write([]byte(tag + " OK Logout completed\r\n"))
			return
		}
	}
}

// parseRequestedUIDs pulls the comma-separated UID list out of a
// "<tag> UID FETCH <uids> (X-GM-LABELS)" command line, for the fake
// server above to echo back per-UID responses.
func parseRequestedUIDs(line string) []int {
	idx := strings.Index(line, "UID FETCH ")
	if idx == -1 {
		return nil
	}
	rest := line[idx+len("UID FETCH "):]
	end := strings.IndexByte(rest, ' ')
	if end == -1 {
		return nil
	}
	var uids []int
	for _, tok := range strings.Split(rest[:end], ",") {
		var uid int
		if _, err := fmt.Sscanf(tok, "%d", &uid); err == nil {
			uids = append(uids, uid)
		}
	}
	return uids
}

type fixedTokenSource string

func (t fixedTokenSource) Token(context.Context, string) (string, error) {
	return string(t), nil
}

func TestStoreGmailLabelsOverRawConnection(t *testing.T) {
	addr, srv := newFakeGmailIMAPServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	cfg := AccountConfig{Name: "com", Username: "alice@example.com", IMAPHost: host, IMAPPort: port, Gmail: true}
	adapter := newAdapter(cfg, nil, fixedTokenSource("ya29.fake"), nil)
	adapter.gmailTLSConfig = &tls.Config{InsecureSkipVerify: true}

	err = adapter.storeGmailLabels(context.Background(), "INBOX", 42, labelOp{add: []string{"Important", "Follow up"}})
	if err != nil {
		t.Fatalf("storeGmailLabels() error = %v", err)
	}

	select {
	case line := <-srv.storedLine:
		want := `UID STORE 42 +X-GM-LABELS ("Important" "Follow up")`
		if !strings.Contains(line, want) {
			t.Errorf("stored line = %q, want it to contain %q", line, want)
		}
	default:
		t.Fatal("server never received a X-GM-LABELS STORE command")
	}
}

// TestAdapterFetchGmailLabelsRaw covers T14(a)'s Gmail half end to end
// over the raw connection: a batched UID FETCH (X-GM-LABELS) against the
// fake Gmail server, parsed back into a UID->labels map with \Inbox
// dropped and every other backslash system label's name kept.
func TestAdapterFetchGmailLabelsRaw(t *testing.T) {
	addr, _ := newFakeGmailIMAPServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	cfg := AccountConfig{Name: "com", Username: "alice@example.com", IMAPHost: host, IMAPPort: port, Gmail: true}
	adapter := newAdapter(cfg, nil, fixedTokenSource("ya29.fake"), nil)
	adapter.gmailTLSConfig = &tls.Config{InsecureSkipVerify: true}

	got, err := adapter.fetchGmailLabelsRaw(context.Background(), "INBOX", []imap.UID{100, 101})
	if err != nil {
		t.Fatalf("fetchGmailLabelsRaw() error = %v", err)
	}

	want := map[imap.UID][]string{
		100: {"Important", "Muy Importante", "bunker-test"},
		101: {"Sent"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fetchGmailLabelsRaw() = %#v, want %#v", got, want)
	}
}
