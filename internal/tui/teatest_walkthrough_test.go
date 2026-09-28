package tui

import (
	"bytes"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/reyer3/bunker-go/internal/core"
)

// sessionRecorder accumulates a running program's output across a
// walkthrough. teatest.WaitFor drains (and discards) whatever it reads on
// every call, so calling it more than once per test silently loses earlier
// frames from any later golden snapshot; this keeps one persistent copy
// (buf) instead, used for wait-condition matching, plus a curated copy
// (golden) used for the golden file — see typeSettled for why they differ.
type sessionRecorder struct {
	r      io.Reader
	buf    bytes.Buffer
	golden bytes.Buffer
	paused bool
}

// drain copies whatever is currently buffered without blocking: the
// underlying reader is a bytes.Buffer, whose Read returns io.EOF once
// empty, so io.Copy returns immediately when there is nothing new yet.
// Newly read bytes always go into buf (for matching); they only go into
// golden while recording isn't paused (see typeSettled).
func (s *sessionRecorder) drain() {
	var chunk bytes.Buffer
	_, _ = io.Copy(&chunk, s.r)
	if chunk.Len() == 0 {
		return
	}
	s.buf.Write(chunk.Bytes())
	if !s.paused {
		s.golden.Write(chunk.Bytes())
	}
}

// waitForText blocks until text appears in output produced after this call
// started, or fails after timeout. It deliberately ignores any earlier
// occurrence already sitting in the accumulated buffer: text like "Mail ("
// (a section header) reappears every time the inbox is shown again (e.g.
// after a send), and matching against the whole cumulative history would
// let an old occurrence satisfy a wait before the new render actually
// happens.
func (s *sessionRecorder) waitForText(t *testing.T, text string, timeout time.Duration) {
	t.Helper()
	start := s.buf.Len()
	deadline := time.Now().Add(timeout)
	for {
		s.drain()
		if bytes.Contains(s.buf.Bytes()[start:], []byte(text)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitForText %q: timed out; new output so far:\n%s", text, s.buf.Bytes()[start:])
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// typeSettled sends text as individual keystrokes (like tm.Type) but keeps
// the resulting burst of intermediate renders out of the golden file: how
// many paint frames a rapid run of keystrokes produces depends on the Go
// scheduler under load, not on application behavior (confirmed by running
// the full suite, where CPU contention from other packages' tests made an
// extra "Attach path: ..." mid-typing frame appear in one run and not
// others), so locking that count into a byte-exact golden is flaky. This
// still exercises the real per-character key dispatch — it only pauses
// which bytes get kept for the golden, not what the program actually does
// — and resumes recording once the output has been quiet for a short
// settle window.
func (s *sessionRecorder) typeSettled(tm *teatest.TestModel, text string) {
	s.paused = true
	defer func() { s.paused = false }()
	tm.Type(text)
	quiet := 0
	for quiet < 4 { // four consecutive 20ms checks with no new bytes
		before := s.buf.Len()
		s.drain()
		if s.buf.Len() == before {
			quiet++
		} else {
			quiet = 0
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWalkthroughNarrowListReadReplyCancel drives a full session through a
// real Bubble Tea program (not just Model.Update): open a mail item's K6
// thread view, start a reply from the full editor, preview it, then
// cancel out at every stage without ever sending, and quit. It runs at a
// narrow terminal width and its recorded output is checked against a
// golden file (see testdata/).
func TestWalkthroughNarrowListReadReplyCancel(t *testing.T) {
	at := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	client := &replyClient{outgoingPreview: core.Plan{
		Channel: core.ChannelMail, Account: "work", Recipients: []string{"alice@example.com"},
	}}
	client.items = []core.Item{item("mail:work:1", core.ChannelMail, "work", "", at)}
	client.threadItems = []core.Item{{
		ID: "mail:work:1", Channel: core.ChannelMail, Account: "work",
		Subject: "Status update", From: core.Address{ID: "alice@example.com", Name: "Alice"},
		Body: "Please review the attached report.", Timestamp: at,
	}}
	// K8 fetches the newest (auto-expanded) message's body on open via
	// Read(id, receipt=false), the same way a real mail sync (headers
	// only until fetched) needs it to; the fake's Read must answer with
	// the same body Thread already carries here for the golden's "Please
	// review..." text to still appear.
	client.readResult = core.Item{Body: "Please review the attached report."}
	// A real network round trip races the renderer for whether a transient
	// frame gets its own flush before the result arrives; a small fixed
	// delay makes that frame reliably observable (and part of the golden)
	// instead of a coin flip under load.
	client.delay = 60 * time.Millisecond

	model := NewModel(client)
	// Pin the clock to the fixture's own day: the golden captures the
	// exact rendered "HH:MM" relative time, which must not depend on the
	// real wall-clock date the suite happens to run on.
	model.now = func() time.Time { return at }
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(36, 16))
	rec := &sessionRecorder{r: tm.Output()}

	rec.waitForText(t, "Cargando bandeja de entrada", 3*time.Second)
	rec.waitForText(t, "abrir", 3*time.Second)
	// Mail wraps this conversation under a collapsible sender row
	// (mail-sender-groups.md): expand it and move onto the nested thread
	// row before Enter opens it, matching what a user sees.
	tm.Send(tea.KeyMsg{Type: tea.KeyRight})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // open the K6 mail thread view
	rec.waitForText(t, "Please review", 3*time.Second)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}) // open the full reply editor
	rec.waitForText(t, "Re: Status update", 3*time.Second)
	rec.typeSettled(tm, "thanks, looking now")
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlS}) // mandatory dry-run preview
	rec.waitForText(t, "alice@example.com", 3*time.Second)

	tm.Send(tea.KeyMsg{Type: tea.KeyEsc}) // preview -> back to editing, nothing sent
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc}) // editor -> discard draft, back to the thread
	rec.waitForText(t, "Please review", 3*time.Second)
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc}) // thread -> back to inbox
	rec.waitForText(t, "abrir", 3*time.Second)
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// WaitFinished only returns once the real tea.Program has torn itself
	// down; a hung or panicking terminal restore would time out here.
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	sends := 0
	for _, c := range client.outgoingCalls {
		if !c.dryRun {
			sends++
		}
	}
	if sends != 0 {
		t.Fatalf("cancel walkthrough sent for real: calls=%+v", client.outgoingCalls)
	}

	rec.drain() // catch the teardown bytes written as the program exits
	if !bytes.Contains(rec.golden.Bytes(), []byte("\x1b[?25h")) {
		t.Fatalf("program did not restore the cursor on exit; output:\n%s", rec.golden.String())
	}
	// The golden is the final model's view, not the raw frame stream: how
	// many frames a run paints depends on the scheduler under load, which
	// made byte-exact stream goldens flaky in full-suite runs. The waits
	// above already assert every intermediate screen.
	teatest.RequireEqualOutput(t, []byte(tm.FinalModel(t).View()))
}

// TestWalkthroughWideListReadReplySend drives list -> reply -> attach ->
// preview -> explicit confirm -> real send, at a wide terminal width, and
// checks the recorded output against its own golden file.
func TestWalkthroughWideListReadReplySend(t *testing.T) {
	at := time.Date(2026, 9, 26, 9, 5, 0, 0, time.UTC)
	path := writeTempFile(t, "notes.txt", "hello")
	client := &replyClient{
		previewOut: core.Plan{Channel: core.ChannelMatrix, Account: "team", Recipients: []string{"#general:example.org"}},
		sendOut:    core.Plan{Channel: core.ChannelMatrix, Account: "team", Recipients: []string{"#general:example.org"}},
	}
	client.items = []core.Item{item("matrix:team:2", core.ChannelMatrix, "team", "", at)}
	// See the narrow test's comment: makes the "Enviando…" transient
	// frame reliably observable instead of racing the renderer under load.
	client.delay = 60 * time.Millisecond

	model := NewModel(client)
	model.now = func() time.Time { return at }
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(100, 30))
	rec := &sessionRecorder{r: tm.Output()}

	rec.waitForText(t, "Cargando bandeja de entrada", 3*time.Second)
	rec.waitForText(t, "abrir", 3*time.Second)
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}) // reply directly from the list
	rec.typeSettled(tm, "see attached notes")
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlA}) // attach
	rec.typeSettled(tm, path)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // confirm the attachment path
	rec.waitForText(t, "notes.txt", 3*time.Second)

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlS}) // mandatory dry-run preview
	rec.waitForText(t, "#general:example.org", 3*time.Second)
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // explicit confirm: real send
	rec.waitForText(t, "Enviando…", 3*time.Second)
	rec.waitForText(t, "abrir", 3*time.Second)
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	if client.sendCalls() != 1 {
		t.Fatalf("send walkthrough calls = %d, want exactly 1: %+v", client.sendCalls(), client.calls)
	}
	last := client.calls[len(client.calls)-1]
	if last.dryRun || len(last.attach) != 1 || last.attach[0] != path {
		t.Fatalf("send call = %+v, want a real send carrying [%q]", last, path)
	}

	rec.drain()
	if !bytes.Contains(rec.golden.Bytes(), []byte("\x1b[?25h")) {
		t.Fatalf("program did not restore the cursor on exit; output:\n%s", rec.golden.String())
	}
	// The golden is the final model's view, not the raw frame stream: how
	// many frames a run paints depends on the scheduler under load, which
	// made byte-exact stream goldens flaky in full-suite runs. The waits
	// above already assert every intermediate screen.
	teatest.RequireEqualOutput(t, []byte(tm.FinalModel(t).View()))
}
