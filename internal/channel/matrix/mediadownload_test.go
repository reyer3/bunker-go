package matrix

import (
	"errors"
	"net/http"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

func TestDownloadMediaUsesAuthenticatedEndpointWhenSupported(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	uri := id.ContentURI{Homeserver: "matrix.example.org", FileID: "v1-file"}
	state.setMediaFile(uri, []byte("v1 bytes"))
	a := newTestAdapter(t, srv, nil)

	data, err := a.downloadMedia(t.Context(), uri)
	if err != nil {
		t.Fatalf("downloadMedia: %v", err)
	}
	if string(data) != "v1 bytes" {
		t.Errorf("data = %q, want %q", data, "v1 bytes")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.v1Downloads != 1 || state.legacyDownloads != 0 {
		t.Errorf("v1Downloads = %d, legacyDownloads = %d, want 1 and 0", state.v1Downloads, state.legacyDownloads)
	}
}

func TestDownloadMediaFallsBackToLegacyAndRemembers(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	uri := id.ContentURI{Homeserver: "matrix.example.org", FileID: "legacy-file"}
	state.setMediaFile(uri, []byte("legacy bytes"))
	state.legacyMediaOnly = true
	a := newTestAdapter(t, srv, nil)

	for i := range 2 {
		data, err := a.downloadMedia(t.Context(), uri)
		if err != nil {
			t.Fatalf("downloadMedia #%d: %v", i+1, err)
		}
		if string(data) != "legacy bytes" {
			t.Errorf("downloadMedia #%d data = %q, want %q", i+1, data, "legacy bytes")
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	// The second download goes straight to the legacy path.
	if state.v1Downloads != 1 || state.legacyDownloads != 2 {
		t.Errorf("v1Downloads = %d, legacyDownloads = %d, want 1 and 2", state.v1Downloads, state.legacyDownloads)
	}
}

func TestDownloadMediaDoesNotRetryOtherErrors(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	uri := id.ContentURI{Homeserver: "matrix.example.org", FileID: "forbidden-file"}
	state.setMediaFile(uri, []byte("secret"))
	state.mediaErr = http.StatusForbidden
	a := newTestAdapter(t, srv, nil)

	_, err := a.downloadMedia(t.Context(), uri)
	if !errors.Is(err, mautrix.MForbidden) {
		t.Fatalf("downloadMedia error = %v, want M_FORBIDDEN", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.v1Downloads != 1 || state.legacyDownloads != 0 {
		t.Errorf("v1Downloads = %d, legacyDownloads = %d, want 1 and 0", state.v1Downloads, state.legacyDownloads)
	}
}

func TestDownloadMediaDoesNotRetryMediaNotFound(t *testing.T) {
	srv, state := newFakeHomeserver(t, nil)
	a := newTestAdapter(t, srv, nil)
	uri := id.ContentURI{Homeserver: "matrix.example.org", FileID: "missing"}
	srv.Config.Handler.(*http.ServeMux).HandleFunc("/_matrix/client/v1/media/download/matrix.example.org/missing", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.v1Downloads++
		state.mu.Unlock()
		writeMatrixNotFound(w)
	})

	_, err := a.downloadMedia(t.Context(), uri)
	if !errors.Is(err, mautrix.MNotFound) {
		t.Fatalf("downloadMedia error = %v, want M_NOT_FOUND", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.legacyDownloads != 0 {
		t.Errorf("legacyDownloads = %d, want 0 (M_NOT_FOUND is not an unknown endpoint)", state.legacyDownloads)
	}
}
