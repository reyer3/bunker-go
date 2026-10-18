package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/purpshell/meowcaller"
)

// errVideoDisabled is returned when a call has no video configured.
var errVideoDisabled = errors.New("whatsapp: video is not configured for this call")

// VideoError is reported when a video helper fails or is missing, like
// AudioError for audio. The short text goes to the user; the full error
// goes to the log.
type VideoError string

func (e VideoError) Error() string { return string(e) }

// videoOpenProblem is the short, user-facing text for a video Open failure.
func videoOpenProblem(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return "falta el programa de video, revisa call_video_capture_command y call_video_playback_command"
	}
	return "no se pudo abrir el video: " + err.Error()
}

// Default video commands: mpv/ffplay read Annex-B H.264 from stdin for
// remote display; ffmpeg v4l2 captures camera and writes Annex-B to stdout.
var (
	defaultVideoCaptureCommand  = []string{"ffmpeg", "-f", "v4l2", "-i", "/dev/video0", "-c:v", "libx264", "-preset", "zerolatency", "-f", "h264", "-"}
	defaultVideoPlaybackCommand = []string{"mpv", "--demuxer=rawvideo", "--rawvideo=1920x1080:fps=30", "-"}
)

// commandVideo implements video capture and playback for one call, like
// commandAudio does for audio. It spawns one playback process (Annex-B
// frames on stdin -> remote display) and/or one capture process (camera
// -> Annex-B frames on stdout).
type commandVideo struct {
	playback []string
	capture  []string
	log      *slog.Logger
}

func (c commandVideo) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default()
}

// Check reports, before any call starts, why this configuration cannot
// have video: a missing capture or playback helper.
func (c commandVideo) Check() error {
	var errs []error
	for _, h := range []struct {
		role string
		argv []string
	}{{"capture", c.capture}, {"playback", c.playback}} {
		if len(h.argv) == 0 {
			continue
		}
		if _, err := exec.LookPath(h.argv[0]); err != nil {
			errs = append(errs, missingVideoHelperError(h.role, h.argv[0]))
		}
	}
	return errors.Join(errs...)
}

// missingVideoHelperError says which video helper is not installed.
func missingVideoHelperError(role, name string) error {
	return fmt.Errorf("whatsapp: call video: %s not found in PATH; install it or set call_video_%s_command", filepathBase(name), role)
}

// Open starts playback and/or capture for the call. playbackStdin receives
// Annex-B frames to play (remote video from the peer). captureStdout
// produces Annex-B frames from the camera (our outgoing video). Either may
// be nil when that direction is disabled.
func (c commandVideo) Open(ctx context.Context, callID string, problem func(string, error)) (playbackStdin io.WriteCloser, captureStdout io.ReadCloser, err error) {
	var wg sync.WaitGroup
	var openErr error

	if len(c.playback) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, in, err := startPlaybackVideo(c.playback, c.logger(), problem)
			if err != nil {
				openErr = err
				return
			}
			playbackStdin = in
		}()
	}
	if len(c.capture) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, out, err := startCaptureVideo(c.capture, c.logger(), problem)
			if err != nil {
				openErr = err
				return
			}
			captureStdout = out
		}()
	}
	wg.Wait()
	if openErr != nil {
		return nil, nil, openErr
	}
	return playbackStdin, captureStdout, nil
}

// callVideo manages video for one live call: it connects the peer's H.264
// (ReceiveVideo) to the playback process's stdin, and the capture process's
// stdout to SendVideo (our outgoing camera).
type callVideo struct {
	live     liveCall
	cmd      commandVideo
	playback io.WriteCloser // stdin of the playback process
	capture  io.ReadCloser  // stdout of the capture process
	done     chan struct{}
	mu       sync.Mutex
	closed   bool
}

// newCallVideo starts video for live. It returns an error if video is not
// configured or if the helpers cannot be opened.
func newCallVideo(live liveCall, cmd commandVideo, problem func(string, error)) (*callVideo, error) {
	ctx := context.Background()
	playbackStdin, captureStdout, err := cmd.Open(ctx, live.ID(), problem)
	if err != nil {
		return nil, err
	}
	v := &callVideo{
		live:     live,
		cmd:      cmd,
		playback: playbackStdin,
		capture:  captureStdout,
		done:     make(chan struct{}),
	}
	// Feed the peer's video into the playback process. When there is no
	// playback process (nil stdin), the sink discards the peer's video.
	if v.playback != nil {
		live.ReceiveVideo(meowcaller.VideoSinkFunc(func(au []byte) {
			v.mu.Lock()
			defer v.mu.Unlock()
			if v.closed || v.playback == nil {
				return
			}
			_, werr := v.playback.Write(au)
			if werr != nil {
				v.cmd.logger().Error("call video playback write failed", "error", werr)
			}
		}))
	} else {
		live.ReceiveVideo(meowcaller.VideoSinkFunc(func([]byte) {}))
	}
	// Push our camera's video to the peer.
	if v.capture != nil {
		go v.pushCapture()
	} else {
		close(v.done)
	}
	// Respond to keyframe requests.
	live.OnVideoKeyframeRequest(func() {
		v.requestKeyframe()
	})
	return v, nil
}

// pushCapture reads Annex-B access units from the capture process and sends
// each one to the peer via SendVideo.
func (v *callVideo) pushCapture() {
	defer close(v.done)
	buf := make([]byte, 0, 65536)
	// Simple Annex-B access unit splitter: each unit starts with a start
	// code (0x000001 or 0x00000001).
	for {
		n, err := v.capture.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if n > 0 {
			units, rest := splitAnnexB(buf)
			for _, au := range units {
				if serr := v.live.SendVideo(au); serr != nil {
					v.cmd.logger().Error("call video send failed", "error", serr)
					return
				}
			}
			buf = rest
		}
		if err != nil {
			if err != io.EOF {
				v.cmd.logger().Error("call video capture ended", "error", err)
			}
			return
		}
	}
}

// splitAnnexB splits buf into complete Annex-B access units (each starting
// with a 3- or 4-byte start code) plus the remainder. It does not require
// the buffer to start at an access unit boundary.
func splitAnnexB(buf []byte) (units [][]byte, rest []byte) {
	rest = buf
	for len(rest) >= 3 {
		i := indexStartCode(rest)
		if i < 0 {
			rest = nil
			break
		}
		if i > 0 {
			rest = rest[i:] // discard leading bytes before the start code
		}
		j := indexStartCode(rest[3:])
		if j < 0 {
			units = append(units, rest)
			rest = nil
		} else {
			units = append(units, rest[:3+j])
			rest = rest[3+j:]
		}
	}
	return units, rest
}

// indexStartCode returns the index of the first Annex-B start code in b,
// or -1 if none. It recognizes both 3-byte (00 00 01) and 4-byte
// (00 00 00 01) start codes.
func indexStartCode(b []byte) int {
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
		if i+4 <= len(b) && b[i] == 0 && b[i+1] == 0 && b[i+2] == 0 && b[i+3] == 1 {
			return i
		}
	}
	return -1
}

// requestKeyframe asks the capture source for a keyframe. With ffmpeg this
// is best-effort; the callback exists so the encoder can be nudged.
func (v *callVideo) requestKeyframe() {
	v.cmd.logger().Info("call video keyframe requested")
}

// Close stops video for this call. Idempotent.
func (v *callVideo) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	playback := v.playback
	capture := v.capture
	v.mu.Unlock()

	if playback != nil {
		_ = playback.Close()
	}
	if capture != nil {
		_ = capture.Close()
	}
	// Wait for pushCapture to finish, but only if it was started (capture
	// was non-nil). When capture is nil, done was already closed in
	// newCallVideo.
	if capture != nil {
		<-v.done
	}
	return nil
}

// filepathBase returns the last path component of path.
func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// commandVideoFromOptions reads an account's video options.
func commandVideoFromOptions(opts map[string]interface{}) commandVideo {
	c := commandVideo{
		playback: stringSliceOption(opts, "call_video_playback_command", defaultVideoPlaybackCommand),
		capture:  stringSliceOption(opts, "call_video_capture_command", defaultVideoCaptureCommand),
	}
	return c
}

// startCaptureVideo runs argv with its stdout piped to the returned reader.
func startCaptureVideo(argv []string, log *slog.Logger, problem func(string, error)) (*videoHelper, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("whatsapp: call video capture: %w", err)
	}
	h, err := startVideoHelper("capture", argv, nil, pw, log, problem)
	_ = pw.Close()
	if err != nil {
		_ = pr.Close()
		return nil, nil, err
	}
	return h, pr, nil
}

// startPlaybackVideo runs argv with its stdin fed from the returned writer.
func startPlaybackVideo(argv []string, log *slog.Logger, problem func(string, error)) (*videoHelper, *os.File, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("whatsapp: call video playback: %w", err)
	}
	h, err := startVideoHelper("playback", argv, pr, nil, log, problem)
	_ = pr.Close()
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	return h, pw, nil
}

// videoHelper is one running video process.
type videoHelper struct {
	role    string
	name    string
	cmd     *exec.Cmd
	stderr  *tailBuffer
	done    chan struct{}
	exitErr error
	closing atomic.Bool
	log     *slog.Logger
	problem func(string, error)
	once    sync.Once
}

func startVideoHelper(role string, argv []string, stdin, stdout *os.File, log *slog.Logger, problem func(string, error)) (*videoHelper, error) {
	h := &videoHelper{
		role:    role,
		name:    filepathBase(argv[0]),
		stderr:  &tailBuffer{max: helperStderrTail},
		done:    make(chan struct{}),
		log:     log.With("helper", filepathBase(argv[0]), "role", role),
		problem: problem,
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	cmd.Stderr = h.stderr
	cmd.WaitDelay = helperWaitDelay
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, missingVideoHelperError(role, argv[0])
		}
		return nil, fmt.Errorf("whatsapp: call video %s %q: %w", role, argv[0], err)
	}
	h.cmd = cmd
	h.log.Info("call video helper started", "args", strings.Join(argv[1:], " "))
	go h.watch()
	return h, nil
}

func (h *videoHelper) watch() {
	err := h.cmd.Wait()
	h.exitErr = err
	close(h.done)
	if h.closing.Load() {
		return
	}
	status := "exit status 0"
	if err != nil {
		status = err.Error()
	}
	h.report(fmt.Errorf("whatsapp: call video %s %s exited early (%s); stderr: %q", h.role, h.name, status, h.stderr.String()))
}

func (h *videoHelper) report(err error) {
	h.once.Do(func() {
		h.log.Error("call video helper failed", "error", err)
		if h.problem != nil {
			h.problem(h.name+": "+err.Error(), err)
		}
	})
}

func (h *videoHelper) close() {
	h.closing.Store(true)
	_ = h.cmd.Process.Kill()
	<-h.done
}
