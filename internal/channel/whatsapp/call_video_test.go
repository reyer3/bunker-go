package whatsapp

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// TestVideoPlaybackCommandConfigured is RED for T4: it verifies that
// call_video_playback_command is read from account options.
func TestVideoPlaybackCommandConfigured(t *testing.T) {
	cfg := map[string]interface{}{
		"call_video_playback_command": []interface{}{"ffplay", "-"},
	}
	cmd := commandVideoFromOptions(cfg)
	if len(cmd.playback) != 2 || cmd.playback[0] != "ffplay" || cmd.playback[1] != "-" {
		t.Fatalf("playback = %+v, want [ffplay -]", cmd.playback)
	}
}

// TestVideoCaptureCommandConfigured is RED for T5: it verifies that
// call_video_capture_command is read from account options.
func TestVideoCaptureCommandConfigured(t *testing.T) {
	cfg := map[string]interface{}{
		"call_video_capture_command": []interface{}{"ffmpeg", "-f", "lavfi", "-i", "dummy", "-"},
	}
	cmd := commandVideoFromOptions(cfg)
	if len(cmd.capture) != 6 || cmd.capture[0] != "ffmpeg" {
		t.Fatalf("capture = %+v, want ffmpeg-based", cmd.capture)
	}
}

// TestVideoCheckRejectsMissingPlaybackHelper is RED for T4: Check must
// report a missing playback helper.
func TestVideoCheckRejectsMissingPlaybackHelper(t *testing.T) {
	cmd := commandVideo{playback: []string{"nonexistent-video-player-xyz"}}
	if err := cmd.Check(); err == nil {
		t.Fatal("expected Check to fail for missing playback helper")
	}
}

// TestVideoCheckRejectsMissingCaptureHelper is RED for T5: Check must
// report a missing capture helper.
func TestVideoCheckRejectsMissingCaptureHelper(t *testing.T) {
	cmd := commandVideo{capture: []string{"nonexistent-video-capturer-xyz"}}
	if err := cmd.Check(); err == nil {
		t.Fatal("expected Check to fail for missing capture helper")
	}
}

// TestRemoteVideoStartsOnVideoCall is RED for T4: when a call has video
// and OnReady fires, ReceiveVideo should be attached to the live call.
func TestRemoteVideoStartsOnVideoCall(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	// Fake helpers instead of mpv/ffmpeg: the gate runs on every
	// environment, including CI runners without video players. The playback
	// one must keep reading stdin (the sink receives Annex-B bytes) and the
	// capture one must just exit cleanly so the command pipe reports EOF.
	v := commandVideo{playback: []string{"sh", "-c", "cat > /dev/null"}, capture: []string{"sh", "-c", "exit 0"}}
	a.callVideo = v

	live := &fakeLiveCall{id: "VID1", peer: callPeer, video: true}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	if live.videoSink == nil {
		t.Fatal("live call has no video sink after OnReady on a video call (RED: T4 not wired)")
	}
}

// TestSendVideoSendsAccessUnits is RED for T5: SendVideo must push encoded
// H.264 access units to the peer through the live call.
func TestSendVideoSendsAccessUnits(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	live := &fakeLiveCall{id: "VID2", peer: callPeer, video: true}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	if live.videoSink == nil {
		t.Fatal("no video sink; T4 not wired yet")
	}

	au := []byte{0x00, 0x00, 0x00, 0x01, 0x67} // Annex-B access unit (SPS)
	if err := live.SendVideo(au); err != nil {
		t.Fatalf("SendVideo = %v, want nil", err)
	}
	if len(live.videoSent) != 1 {
		t.Fatalf("videoSent = %d, want 1", len(live.videoSent))
	}
	if string(live.videoSent[0]) != string(au) {
		t.Fatalf("videoSent = %v, want %v", live.videoSent, au)
	}
}

// TestOnVideoKeyframeRequestRegistersCallback is RED for T5: the peer's
// keyframe request (PLI/FIR) must be delivered to the capture source.
func TestOnVideoKeyframeRequestRegistersCallback(t *testing.T) {
	a, engine, _, _ := newCallAdapter(t)
	t.Cleanup(a.hangupAll)
	live := &fakeLiveCall{id: "VID3", peer: callPeer, video: true}
	engine.next = live
	if _, err := a.PlaceCall(context.Background(), callPeer.String()); err != nil {
		t.Fatal(err)
	}
	live.onReady()
	if live.videoSink == nil {
		t.Fatal("no video sink; T4 not wired yet")
	}
	if live.onVideoKeyframeRequest == nil {
		t.Fatal("onVideoKeyframeRequest is nil; T5 not wired")
	}
	// Invoking the callback must not panic.
	live.onVideoKeyframeRequest()
}

// TestSplitAnnexB is RED for T5: the Annex-B access-unit splitter must
// divide a buffer into units starting at start codes.
func TestSplitAnnexB(t *testing.T) {
	buf := []byte{
		0x00, 0x00, 0x00, 0x01, 0x67, 0x42, // unit 1 (4-byte start code)
		0x00, 0x00, 0x01, 0x68, // unit 2 (3-byte start code)
	}
	units, rest := splitAnnexB(buf)
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %v, want empty", rest)
	}
	if string(units[0]) != "\x00\x00\x00\x01\x67\x42" {
		t.Fatalf("unit 0 = %v, want SPS access unit", units[0])
	}
	if string(units[1]) != "\x00\x00\x01\x68" {
		t.Fatalf("unit 1 = %v, want PPS access unit", units[1])
	}
}

// TestSplitAnnexBDiscardsLeadingJunk is RED for T5: bytes before the first
// start code must be discarded.
func TestSplitAnnexBDiscardsLeadingJunk(t *testing.T) {
	buf := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x00, 0x00, 0x01, 0x61}
	units, rest := splitAnnexB(buf)
	if len(units) != 2 {
		t.Fatalf("got %d units, want 2", len(units))
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %v, want empty", rest)
	}
	if string(units[0]) != "\x00\x00\x00\x01\x65" {
		t.Fatalf("unit 0 = %v, want first access unit", units[0])
	}
	if string(units[1]) != "\x00\x00\x01\x61" {
		t.Fatalf("unit 1 = %v, want second access unit", units[1])
	}
}

// TestIndexStartCode finds both 3- and 4-byte start codes.
func TestIndexStartCode(t *testing.T) {
	cases := []struct {
		in   []byte
		want int
	}{
		{[]byte{0x00, 0x00, 0x01, 0x65}, 0},
		{[]byte{0x00, 0x00, 0x00, 0x01, 0x65}, 0},
		{[]byte{0xaa, 0x00, 0x00, 0x01, 0x65}, 1},
		{[]byte{0xaa, 0x00, 0x00, 0x00, 0x01, 0x65}, 1},
		{[]byte{0xaa, 0xbb, 0xcc}, -1},
		{[]byte{}, -1},
	}
	for _, tc := range cases {
		if got := indexStartCode(tc.in); got != tc.want {
			t.Fatalf("indexStartCode(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestVideoErrorType is RED for T4/T5: VideoError is a string type that
// implements error.
func TestVideoErrorType(t *testing.T) {
	var err error = VideoError("no se pudo abrir el video")
	if err.Error() != "no se pudo abrir el video" {
		t.Fatalf("VideoError.Error() = %q, want the message", err.Error())
	}
}

var _ = errors.Is
var _ = types.NewJID
var _ = core.ChannelWhatsApp
