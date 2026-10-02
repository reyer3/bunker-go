package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/reyer3/bunker-go/internal/core"
)

func TestCallBannerShowsAudioError(t *testing.T) {
	model := callInbox(&callClient{})
	active := ringingCall()
	active.State = core.CallStateActive
	active.ConnectedAt = callsNow.Add(-42 * time.Second)
	active.AudioError = "pacat no encontrado"
	model, _ = deliverCalls(model, active)
	want := "En llamada con Alice · 0:42 · sin audio: pacat no encontrado · h colgar"
	if line := lastLine(model.View()); !strings.Contains(line, want) {
		t.Fatalf("status line = %q, want %q", line, want)
	}

	connecting := active
	connecting.State = core.CallStateConnecting
	connecting.AudioError = "no llega audio del otro lado (sin medios)"
	model, _ = deliverCalls(model, connecting)
	if line := lastLine(model.View()); !strings.Contains(line, "Conectando con Alice… · sin audio: no llega audio del otro lado (sin medios) · h colgar") {
		t.Fatalf("connecting line = %q", line)
	}
}
