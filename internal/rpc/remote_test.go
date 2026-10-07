package rpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/store"
)

// remoteTestDaemon is a real Server over a real store whose fake mail
// adapter serves data as item mail:cl:att's attachment 0, with a staging
// dir and an avatar cache dir of its own.
type remoteTestDaemon struct {
	socket     string
	stagingDir string
	avatarDir  string
	adapter    *fake.Adapter
}

func startRemoteTestDaemon(t *testing.T, data []byte) remoteTestDaemon {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bunker.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	item := core.Item{
		ID:          "mail:cl:att",
		Channel:     core.ChannelMail,
		Account:     "cl",
		Attachments: []core.Attachment{{Name: "big.bin", MIME: "application/octet-stream", Size: int64(len(data))}},
	}
	if err := st.Upsert(context.Background(), item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	reg := core.NewRegistry()
	adapter := fake.New(core.ChannelMail, "cl")
	adapter.SetAttachmentData(item.ID, 0, data)
	reg.Register(adapter)
	svc := core.NewService(st, reg)
	d := remoteTestDaemon{
		socket:     filepath.Join(dir, "bunker.sock"),
		stagingDir: filepath.Join(dir, "staging"),
		avatarDir:  filepath.Join(dir, "avatars"),
		adapter:    adapter,
	}
	svc.SetStagingDir(d.stagingDir)
	svc.SetAvatarCacheDir(d.avatarDir)

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- NewServer(svc).Serve(ctx, d.socket) }()
	t.Cleanup(func() {
		cancel()
		<-serveErr
	})
	return d
}

// startScriptedServer answers every request with handle, so a test can
// play an older daemon, a daemon on another machine or a lying one.
func startScriptedServer(t *testing.T, handle func(Request) Response) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
				enc := json.NewEncoder(conn)
				for sc.Scan() {
					var req Request
					if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
						return
					}
					resp := handle(req)
					resp.ID = req.ID
					if err := enc.Encode(resp); err != nil {
						return
					}
				}
			}()
		}
	}()
	return socket
}

func dialRemoteTest(t *testing.T, socket string) *Client {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("server never became reachable")
	return nil
}

func mustResult(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRemoteClientDownloadsLargeAttachmentInChunks is the headline case:
// a client on another machine (forced with BUNKER_REMOTE=1) downloads an
// attachment larger than the protocol's 8 MB line cap to a path only it
// can see. It only fits through the socket in chunks.
func TestRemoteClientDownloadsLargeAttachmentInChunks(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	data := make([]byte, 9<<20+12345)
	for i := range data {
		data[i] = byte(i*7 + i>>11)
	}
	d := startRemoteTestDaemon(t, data)
	client := dialRemoteTest(t, d.socket)

	dest := filepath.Join(t.TempDir(), "big.bin")
	res, err := client.Download(context.Background(), "mail:cl:att", 0, dest, core.DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.Path != dest || res.Bytes != int64(len(data)) || res.Name != "big.bin" || res.MIME != "application/octet-stream" {
		t.Fatalf("Download = %+v, want Path=%s Bytes=%d big.bin", res, dest, len(data))
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("downloaded %d bytes differ from the %d attachment bytes", len(got), len(data))
	}
	if info, _ := os.Stat(dest); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(d.stagingDir)
	if err != nil {
		t.Fatalf("ReadDir(staging): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging dir holds %d entries after the download, want the token released", len(entries))
	}

	// The local writer still refuses to overwrite without Force.
	if _, err := client.Download(context.Background(), "mail:cl:att", 0, dest, core.DownloadOptions{}); !errors.Is(err, core.ErrDestinationExists) {
		t.Fatalf("second Download err = %v, want ErrDestinationExists", err)
	}
	if entries, _ := os.ReadDir(d.stagingDir); len(entries) != 0 {
		t.Fatalf("staging dir holds %d entries after a refused download", len(entries))
	}
}

func TestClientDetectsSameMachineDaemonAsLocal(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	remote, err := client.isRemote(context.Background())
	if err != nil || remote {
		t.Fatalf("isRemote = %v, %v; want local (the daemon reads the probe file)", remote, err)
	}
}

func TestClientRemoteDetection(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		probe      func(fsProbeParams) Response
		wantRemote bool
		wantErr    bool
		wantProbes int
	}{
		{
			name: "daemon cannot read the probe file",
			probe: func(fsProbeParams) Response {
				return Response{Result: json.RawMessage(`{"readable":false}`)}
			},
			wantRemote: true,
			wantProbes: 1,
		},
		{
			name: "older daemon without fs_probe stays local",
			probe: func(fsProbeParams) Response {
				return Response{Error: `rpc: unknown method "fs_probe"`}
			},
			wantRemote: false,
			wantProbes: 1,
		},
		{
			name: "probe failure is an error, not a guess",
			probe: func(fsProbeParams) Response {
				return Response{Error: "rpc: something broke"}
			},
			wantErr:    true,
			wantProbes: 1,
		},
		{name: "BUNKER_REMOTE=1 forces remote", env: "1", wantRemote: true},
		{name: "BUNKER_REMOTE=0 forces local", env: "0", wantRemote: false},
		{name: "invalid BUNKER_REMOTE fails loudly", env: "maybe", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BUNKER_REMOTE", tt.env)
			var mu sync.Mutex
			probes := 0
			socket := startScriptedServer(t, func(req Request) Response {
				if req.Method != MethodFSProbe {
					return Response{Error: "unexpected method " + req.Method}
				}
				var p fsProbeParams
				_ = json.Unmarshal(req.Params, &p)
				mu.Lock()
				probes++
				mu.Unlock()
				if _, err := os.Stat(p.Path); err != nil {
					t.Errorf("probe file %s missing during the probe: %v", p.Path, err)
				}
				return tt.probe(p)
			})
			client := dialRemoteTest(t, socket)
			remote, err := client.isRemote(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("isRemote err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if remote != tt.wantRemote {
					t.Fatalf("isRemote = %v, want %v", remote, tt.wantRemote)
				}
				// The decision is cached: no second probe.
				if again, _ := client.isRemote(context.Background()); again != remote {
					t.Fatalf("second isRemote = %v, want cached %v", again, remote)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if probes != tt.wantProbes {
				t.Fatalf("probes = %d, want %d", probes, tt.wantProbes)
			}
		})
	}
}

func TestServerFSProbeNeverDisclosesContent(t *testing.T) {
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	path := filepath.Join(t.TempDir(), "probe")
	nonce := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(nonce+"extra"), 0o600); err != nil {
		t.Fatal(err)
	}
	var res json.RawMessage
	if err := client.call(context.Background(), MethodFSProbe, fsProbeParams{Path: path, Nonce: nonce}, &res); err != nil {
		t.Fatalf("fs_probe: %v", err)
	}
	if string(res) != `{"readable":false}` {
		t.Fatalf("fs_probe of a longer file = %s, want exactly {\"readable\":false}", res)
	}
	if err := client.call(context.Background(), MethodFSProbe, fsProbeParams{Path: path, Nonce: "short"}, &res); err == nil {
		t.Fatal("fs_probe with a short nonce succeeded, want an error")
	}
}

func TestRemoteDownloadRejectsChecksumMismatch(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	data := []byte("tampered bytes")
	wrong := sha256.Sum256([]byte("original bytes"))
	var mu sync.Mutex
	closed := false
	socket := startScriptedServer(t, func(req Request) Response {
		switch req.Method {
		case MethodDownloadOpen:
			return Response{Result: mustResult(t, downloadOpenResult{Token: "tok", Size: int64(len(data)), Name: "a.bin", SHA256: hex.EncodeToString(wrong[:])})}
		case MethodDownloadChunk:
			var p downloadChunkParams
			_ = json.Unmarshal(req.Params, &p)
			end := min(p.Offset+p.Length, int64(len(data)))
			return Response{Result: mustResult(t, downloadChunkResult{Data: data[p.Offset:end]})}
		case MethodDownloadClose:
			mu.Lock()
			closed = true
			mu.Unlock()
			return Response{Result: json.RawMessage(`{}`)}
		}
		return Response{Error: "unexpected method " + req.Method}
	})
	client := dialRemoteTest(t, socket)

	dest := filepath.Join(t.TempDir(), "a.bin")
	if _, err := client.Download(context.Background(), "mail:cl:att", 0, dest, core.DownloadOptions{}); err == nil {
		t.Fatal("Download succeeded despite a sha256 mismatch")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dest after a mismatch: %v, want no file", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 0 {
		t.Fatalf("dest dir holds %d entries after a mismatch, want no temp file left", len(entries))
	}
	mu.Lock()
	defer mu.Unlock()
	if !closed {
		t.Fatal("download_close was never called after the failure")
	}
}

func TestRemoteClientAvatarWritesLocalCopy(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	client.avatarDir = filepath.Join(t.TempDir(), "client-avatars")

	res, err := client.Avatar(context.Background(), core.ChannelMail, "cl", "thread-1")
	if err != nil {
		t.Fatalf("Avatar: %v", err)
	}
	if !res.Generated {
		t.Errorf("Generated = false, want true (mail has no AvatarProvider)")
	}
	if filepath.Dir(res.Path) != client.avatarDir {
		t.Fatalf("Path = %s, want a file in the client's own cache %s", res.Path, client.avatarDir)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("ReadFile(local avatar): %v", err)
	}
	daemonCopy, err := filepath.Glob(filepath.Join(d.avatarDir, "*.png"))
	if err != nil || len(daemonCopy) != 1 {
		t.Fatalf("daemon avatar cache = %v, %v; want one png", daemonCopy, err)
	}
	want, _ := os.ReadFile(daemonCopy[0])
	if !bytes.Equal(got, want) || len(got) == 0 {
		t.Fatalf("local avatar (%d bytes) differs from the daemon's (%d bytes)", len(got), len(want))
	}
	if info, _ := os.Stat(res.Path); info.Mode().Perm() != 0o600 {
		t.Fatalf("local avatar mode = %v, want 0600", info.Mode().Perm())
	}

	// A second call overwrites the cached copy rather than failing.
	if _, err := client.Avatar(context.Background(), core.ChannelMail, "cl", "thread-1"); err != nil {
		t.Fatalf("second Avatar: %v", err)
	}
}

// TestRemoteClientRefusesToSendFiles covers the send side until upload
// support lands: the daemon would open these paths on its own machine
// (and send its own file if the same path exists there), so a remote
// client refuses before the daemon hears anything, dry runs included.
func TestRemoteClientRefusesToSendFiles(t *testing.T) {
	tests := []struct {
		name string
		send func(context.Context, *Client) error
	}{
		{"send with an attachment", func(ctx context.Context, c *Client) error {
			out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Body: "x", Attachments: []string{"/home/u/a.pdf"}}
			_, _, err := c.Send(ctx, out, false)
			return err
		}},
		{"dry-run send with an attachment", func(ctx context.Context, c *Client) error {
			out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Attachments: []string{"/home/u/a.pdf"}}
			_, _, err := c.Send(ctx, out, true)
			return err
		}},
		{"reply with an attachment", func(ctx context.Context, c *Client) error {
			_, _, err := c.Reply(ctx, "mail:cl:att", "x", nil, []string{"/home/u/a.pdf"}, true)
			return err
		}},
		{"reply with a voice note", func(ctx context.Context, c *Client) error {
			_, _, err := c.Reply(core.WithVoice(ctx), "mail:cl:att", "", nil, []string{"/home/u/note.ogg"}, false)
			return err
		}},
		{"status with media", func(ctx context.Context, c *Client) error {
			_, _, err := c.PostStatus(ctx, core.ChannelMail, "cl", core.Status{Text: "x", Media: "/home/u/p.jpg"}, true)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BUNKER_REMOTE", "1")
			var mu sync.Mutex
			var calls []string
			socket := startScriptedServer(t, func(req Request) Response {
				mu.Lock()
				calls = append(calls, req.Method)
				mu.Unlock()
				return Response{Result: json.RawMessage(`{}`)}
			})
			client := dialRemoteTest(t, socket)
			err := tt.send(context.Background(), client)
			if !errors.Is(err, core.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 0 {
				t.Fatalf("daemon received %v, want no call at all", calls)
			}
		})
	}
}

func TestRemoteClientStillSendsText(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Subject: "s", Body: "text only"}
	if _, receipt, err := client.Send(context.Background(), out, false); err != nil || receipt.ID == "" {
		t.Fatalf("Send = %+v, %v; want a receipt", receipt, err)
	}
	if sent := d.adapter.SentMessages(); len(sent) != 1 || sent[0].Body != "text only" {
		t.Fatalf("adapter sent %+v, want the one text message", sent)
	}
}

func TestLocalClientStillPassesFilesToDaemon(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "0")
	var mu sync.Mutex
	var calls []string
	socket := startScriptedServer(t, func(req Request) Response {
		mu.Lock()
		calls = append(calls, req.Method)
		mu.Unlock()
		return Response{Result: json.RawMessage(`{}`)}
	})
	client := dialRemoteTest(t, socket)
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Attachments: []string{"/home/u/a.pdf"}}
	if _, _, err := client.Send(context.Background(), out, true); err != nil {
		t.Fatalf("Send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != MethodSend {
		t.Fatalf("daemon received %v, want one send", calls)
	}
}

func TestRemoteSendGuardReturnsDetectionErrors(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "maybe")
	socket := startScriptedServer(t, func(Request) Response { return Response{Result: json.RawMessage(`{}`)} })
	client := dialRemoteTest(t, socket)
	_, _, err := client.Reply(context.Background(), "mail:cl:att", "x", nil, []string{"/home/u/a.pdf"}, true)
	if err == nil || errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("err = %v, want the BUNKER_REMOTE parse error as is", err)
	}
}
