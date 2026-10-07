package rpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
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
	media      *mediaFake
}

// sentFile is one file the daemon handed an adapter: its path there and
// the bytes it held at that moment (the upload is released right after).
type sentFile struct {
	path string
	data []byte
}

// mediaFake adds media, voice and status capture to the fake adapter,
// reading each file while the send is in progress, so a test sees
// exactly what the daemon would have delivered.
type mediaFake struct {
	*fake.Adapter
	mu       sync.Mutex
	media    []sentFile
	voice    []sentFile
	statuses []sentFile
}

func readSent(paths ...string) ([]sentFile, error) {
	var out []sentFile
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, sentFile{path: p, data: data})
	}
	return out, nil
}

func (m *mediaFake) SendMedia(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	files, err := readSent(out.Attachments...)
	if err != nil {
		return core.Receipt{}, err
	}
	m.mu.Lock()
	m.media = append(m.media, files...)
	m.mu.Unlock()
	return m.Adapter.Send(ctx, out)
}

func (m *mediaFake) SendVoice(ctx context.Context, out core.Outgoing) (core.Receipt, error) {
	files, err := readSent(out.Attachments...)
	if err != nil {
		return core.Receipt{}, err
	}
	m.mu.Lock()
	m.voice = append(m.voice, files...)
	m.mu.Unlock()
	return m.Adapter.SendVoice(ctx, out)
}

func (m *mediaFake) PostStatus(ctx context.Context, status core.Status) (core.Receipt, error) {
	files, err := readSent(status.Media)
	if err != nil {
		return core.Receipt{}, err
	}
	m.mu.Lock()
	m.statuses = append(m.statuses, files...)
	m.mu.Unlock()
	return m.Adapter.PostStatus(ctx, status)
}

func (m *mediaFake) AttachmentPolicy() core.AttachmentPolicy {
	return core.AttachmentPolicy{MaxBytes: map[string]int64{core.AnyMIME: core.MaxUploadBytes}}
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
	media := &mediaFake{Adapter: adapter}
	reg.Register(media)
	svc := core.NewService(st, reg)
	d := remoteTestDaemon{
		socket:     filepath.Join(dir, "bunker.sock"),
		stagingDir: filepath.Join(dir, "staging"),
		avatarDir:  filepath.Join(dir, "avatars"),
		adapter:    adapter,
		media:      media,
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

// writeClientFile writes data under name in a dir only the client side
// of a test uses, standing in for a file on the laptop.
func writeClientFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertStagingEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadDir(staging): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging dir holds %d entries after the send, want every upload released", len(entries))
	}
}

// TestRemoteClientSendsLargeAttachmentThroughUploads is the headline
// upload case: a client on another machine sends a file larger than the
// protocol's 8 MB line cap that only it can read. It reaches the daemon
// in chunks, under its original name, byte for byte.
func TestRemoteClientSendsLargeAttachmentThroughUploads(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	data := make([]byte, 9<<20+4321)
	for i := range data {
		data[i] = byte(i*13 + i>>9)
	}
	local := writeClientFile(t, "Informe final.pdf", data)

	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Subject: "s", Body: "see attached", Attachments: []string{local}}
	plan, receipt, err := client.Send(context.Background(), out, false)
	if err != nil || receipt.ID == "" {
		t.Fatalf("Send = %+v, %v; want a receipt", receipt, err)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].Name != "Informe final.pdf" || plan.Attachments[0].Size != int64(len(data)) {
		t.Fatalf("plan attachments = %+v, want Informe final.pdf of %d bytes", plan.Attachments, len(data))
	}
	if len(plan.Media) != 1 || plan.Media[0] != local {
		t.Fatalf("plan media = %v, want the client's own path %s", plan.Media, local)
	}
	d.media.mu.Lock()
	sent := d.media.media
	d.media.mu.Unlock()
	if len(sent) != 1 || filepath.Base(sent[0].path) != "Informe final.pdf" || !bytes.Equal(sent[0].data, data) {
		t.Fatalf("adapter got %d files, want Informe final.pdf with the %d client bytes", len(sent), len(data))
	}
	if !strings.HasPrefix(sent[0].path, d.stagingDir+string(filepath.Separator)) {
		t.Fatalf("adapter opened %s, want a file in the daemon's staging dir", sent[0].path)
	}
	assertStagingEmpty(t, d.stagingDir)
}

func TestRemoteClientRepliesWithVoiceNote(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	ogg := oggfixture.Bytes(3 * time.Second)
	local := writeClientFile(t, "nota.ogg", ogg)

	plan, _, err := client.Reply(core.WithVoice(context.Background()), "mail:cl:att", "", nil, []string{local}, false)
	if err != nil {
		t.Fatalf("Reply voice: %v", err)
	}
	if !plan.Voice || len(plan.Attachments) != 1 || plan.Attachments[0].Name != "nota.ogg" || plan.Attachments[0].DurationMS != 3000 {
		t.Fatalf("plan = %+v, want a 3 s voice note nota.ogg", plan)
	}
	d.media.mu.Lock()
	voice := d.media.voice
	d.media.mu.Unlock()
	if len(voice) != 1 || filepath.Base(voice[0].path) != "nota.ogg" || !bytes.Equal(voice[0].data, ogg) {
		t.Fatalf("adapter got %d voice notes, want nota.ogg with the client bytes", len(voice))
	}
	assertStagingEmpty(t, d.stagingDir)
}

func TestRemoteClientPostsStatusMedia(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	data := []byte("\x89PNG\r\n\x1a\nnot really a picture")
	local := writeClientFile(t, "foto.png", data)

	if _, _, err := client.PostStatus(context.Background(), core.ChannelMail, "cl", core.Status{Text: "hola", Media: local}, false); err != nil {
		t.Fatalf("PostStatus: %v", err)
	}
	d.media.mu.Lock()
	statuses := d.media.statuses
	d.media.mu.Unlock()
	if len(statuses) != 1 || filepath.Base(statuses[0].path) != "foto.png" || !bytes.Equal(statuses[0].data, data) {
		t.Fatalf("adapter got %d status files, want foto.png with the client bytes", len(statuses))
	}
	assertStagingEmpty(t, d.stagingDir)
}

// TestRemoteClientDryRunUploadsForThePlan: the daemon inspects files to
// build a plan, so a dry run uploads them too, sends nothing, and leaves
// no upload behind.
func TestRemoteClientDryRunUploadsForThePlan(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	d := startRemoteTestDaemon(t, []byte("x"))
	client := dialRemoteTest(t, d.socket)
	local := writeClientFile(t, "presupuesto.txt", []byte("total: 10"))

	plan, _, err := client.Reply(context.Background(), "mail:cl:att", "x", nil, []string{local}, true)
	if err != nil {
		t.Fatalf("dry-run Reply: %v", err)
	}
	if len(plan.Attachments) != 1 || plan.Attachments[0].Name != "presupuesto.txt" || plan.Attachments[0].Size != 9 {
		t.Fatalf("plan attachments = %+v, want presupuesto.txt of 9 bytes", plan.Attachments)
	}
	if len(plan.Media) != 1 || plan.Media[0] != local {
		t.Fatalf("plan media = %v, want the client's own path", plan.Media)
	}
	d.media.mu.Lock()
	n := len(d.media.media)
	d.media.mu.Unlock()
	if n != 0 || len(d.adapter.SentMessages()) != 0 {
		t.Fatal("a dry run sent something")
	}
	assertStagingEmpty(t, d.stagingDir)
}

// TestDaemonRefusesStagingPathThatIsNotAnUpload: whatever the client, a
// path into the daemon's staging dir that no upload committed (here a
// download token's file) is refused, so nobody can make the daemon send
// another attachment it parked there.
func TestDaemonRefusesStagingPathThatIsNotAnUpload(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "0")
	d := startRemoteTestDaemon(t, []byte("parked attachment"))
	client := dialRemoteTest(t, d.socket)
	if err := os.MkdirAll(d.stagingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	parked := filepath.Join(d.stagingDir, "0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(parked, []byte("parked attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Body: "x", Attachments: []string{parked}}
	if _, _, err := client.Send(context.Background(), out, false); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Send err = %v, want ErrNotFound", err)
	}
	d.media.mu.Lock()
	defer d.media.mu.Unlock()
	if len(d.media.media) != 0 {
		t.Fatal("the daemon sent its own staged file")
	}
}

// scriptedUploads plays a daemon that accepts uploads, recording every
// method; commitErr fails the commit of the n-th upload (1-based, 0:
// never).
func scriptedUploads(t *testing.T, failCommit int) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	opened := 0
	socket := startScriptedServer(t, func(req Request) Response {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, req.Method)
		switch req.Method {
		case MethodUploadOpen:
			opened++
			return Response{Result: mustResult(t, uploadOpenResult{Token: fmt.Sprintf("%032x", opened)})}
		case MethodUploadCommit:
			var p uploadCommitParams
			_ = json.Unmarshal(req.Params, &p)
			if failCommit > 0 && p.Token == fmt.Sprintf("%032x", failCommit) {
				return Response{Error: "core: upload sha256 mismatch"}
			}
			return Response{Result: mustResult(t, uploadCommitResult{Path: "/daemon/staging/" + p.Token + "/f"})}
		}
		return Response{Result: json.RawMessage(`{}`)}
	})
	return socket, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func TestRemoteClientMissingFileSendsNothing(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	socket, calls := scriptedUploads(t, 0)
	client := dialRemoteTest(t, socket)
	ok := writeClientFile(t, "ok.txt", []byte("ok"))
	missing := filepath.Join(t.TempDir(), "missing.pdf")
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Attachments: []string{ok, missing}}
	_, _, err := client.Send(context.Background(), out, false)
	if err == nil || !strings.Contains(err.Error(), "missing.pdf") {
		t.Fatalf("err = %v, want an error naming missing.pdf", err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("daemon received %v, want nothing (the file check comes first)", got)
	}
}

func TestRemoteClientUploadFailureAbortsAndReleases(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	socket, calls := scriptedUploads(t, 2)
	client := dialRemoteTest(t, socket)
	a := writeClientFile(t, "a.txt", []byte("aaa"))
	b := writeClientFile(t, "b.txt", []byte("bbb"))
	out := core.Outgoing{Channel: core.ChannelMail, Account: "cl", To: []string{"to@example.test"}, Attachments: []string{a, b}}
	if _, _, err := client.Send(context.Background(), out, false); err == nil {
		t.Fatal("Send succeeded although an upload failed")
	}
	got := calls()
	releases := 0
	for _, m := range got {
		if m == MethodSend {
			t.Fatalf("daemon received send after a failed upload: %v", got)
		}
		if m == MethodUploadRelease {
			releases++
		}
	}
	if releases != 2 {
		t.Fatalf("calls = %v, want both uploads released", got)
	}
}

func TestRemoteClientReleasesUploadsAfterSendError(t *testing.T) {
	t.Setenv("BUNKER_REMOTE", "1")
	var mu sync.Mutex
	var calls []string
	socket := startScriptedServer(t, func(req Request) Response {
		mu.Lock()
		calls = append(calls, req.Method)
		mu.Unlock()
		switch req.Method {
		case MethodUploadOpen:
			return Response{Result: mustResult(t, uploadOpenResult{Token: "0123456789abcdef0123456789abcdef"})}
		case MethodUploadCommit:
			return Response{Result: mustResult(t, uploadCommitResult{Path: "/daemon/staging/x/a.txt"})}
		case MethodPostStatus:
			var p statusParams
			_ = json.Unmarshal(req.Params, &p)
			if p.Status.Media != "/daemon/staging/x/a.txt" {
				return Response{Error: "status media " + p.Status.Media + " was not rewritten"}
			}
			return Response{Error: "adapter down"}
		}
		return Response{Result: json.RawMessage(`{}`)}
	})
	client := dialRemoteTest(t, socket)
	local := writeClientFile(t, "a.txt", []byte("aaa"))
	_, _, err := client.PostStatus(context.Background(), core.ChannelMail, "cl", core.Status{Media: local}, false)
	if err == nil || !strings.Contains(err.Error(), "adapter down") {
		t.Fatalf("err = %v, want the daemon's send error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if last := calls[len(calls)-1]; last != MethodUploadRelease {
		t.Fatalf("calls = %v, want the upload released after the failed status", calls)
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
