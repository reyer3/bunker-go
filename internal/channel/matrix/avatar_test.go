package matrix

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"

	"github.com/reyer3/bunker-go/internal/core"
)

// T16(c): Matrix avatars. This file's fake homeserver is self-contained
// (like media_test.go's), independent of adapter_test.go's own
// fakeState/newFakeHomeserver, so a parallel writer touching those never
// conflicts with it.

type avatarFakeState struct {
	mu sync.Mutex

	roomAvatarURL   string // "" => the room has no m.room.avatar (404 M_NOT_FOUND)
	heroAvatarURL   string // "" => the hero has no global avatar_url (404 M_NOT_FOUND)
	roomAvatarErr   int    // non-zero HTTP status forces a non-404 error from the state endpoint
	mediaByID       map[string][]byte
	mediaErr        int // non-zero HTTP status forces a media download error
	downloadedFiles []string
}

func writeMatrixNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]string{"errcode": "M_NOT_FOUND", "error": "not found"})
}

func newAvatarFakeHomeserver(t *testing.T, state *avatarFakeState) *httptest.Server {
	t.Helper()
	if state.mediaByID == nil {
		state.mediaByID = make(map[string][]byte)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/rooms/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/state/m.room.avatar/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.roomAvatarErr != 0 {
			w.WriteHeader(state.roomAvatarErr)
			json.NewEncoder(w).Encode(map[string]string{"errcode": "M_UNKNOWN", "error": "boom"})
			return
		}
		if state.roomAvatarURL == "" {
			writeMatrixNotFound(w)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"url": state.roomAvatarURL})
	})
	mux.HandleFunc("/_matrix/client/v3/profile/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/avatar_url") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.heroAvatarURL == "" {
			writeMatrixNotFound(w)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"avatar_url": state.heroAvatarURL})
	})
	mux.HandleFunc("/_matrix/client/v1/media/download/", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.downloadedFiles = append(state.downloadedFiles, r.URL.Path)
		if state.mediaErr != 0 {
			w.WriteHeader(state.mediaErr)
			return
		}
		parts := strings.Split(r.URL.Path, "/")
		fileID := parts[len(parts)-1]
		w.Write(state.mediaByID[fileID])
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newAvatarTestAdapter(t *testing.T, srv *httptest.Server) *Adapter {
	t.Helper()
	client, err := mautrix.NewClient(srv.URL, id.UserID("@alice:matrix.example.org"), "syt_test_token")
	if err != nil {
		t.Fatalf("mautrix.NewClient: %v", err)
	}
	client.DeviceID = "DEVICE1"
	return newAdapter("work", client, nil)
}

func TestAvatarFetchesRoomAvatar(t *testing.T) {
	state := &avatarFakeState{
		roomAvatarURL: "mxc://matrix.example.org/roompic",
		mediaByID:     map[string][]byte{"roompic": []byte("room-avatar-bytes")},
	}
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	roomID := id.RoomID("!room1:matrix.example.org")
	a.mu.Lock()
	a.roomNames[roomID] = "Widget Team"
	a.mu.Unlock()

	src, ok, err := a.Avatar(t.Context(), roomID.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !ok {
		t.Fatal("Avatar() ok = false, want true")
	}
	if string(src.Data) != "room-avatar-bytes" {
		t.Errorf("Data = %q, want %q", src.Data, "room-avatar-bytes")
	}
	if src.DisplayName != "Widget Team" {
		t.Errorf("DisplayName = %q, want %q", src.DisplayName, "Widget Team")
	}
	if len(state.downloadedFiles) != 1 || !strings.HasSuffix(state.downloadedFiles[0], "/roompic") {
		t.Errorf("downloaded files = %+v, want one download of roompic", state.downloadedFiles)
	}
}

func TestAvatarFallsBackToDMHeroAvatarWhenRoomHasNone(t *testing.T) {
	state := &avatarFakeState{
		heroAvatarURL: "mxc://matrix.example.org/heropic",
		mediaByID:     map[string][]byte{"heropic": []byte("hero-avatar-bytes")},
	}
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	roomID := id.RoomID("!dm1:matrix.example.org")
	hero := id.UserID("@bob:matrix.example.org")
	a.mu.Lock()
	a.heroes[roomID] = []id.UserID{hero}
	a.mu.Unlock()

	src, ok, err := a.Avatar(t.Context(), roomID.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !ok {
		t.Fatal("Avatar() ok = false, want true")
	}
	if string(src.Data) != "hero-avatar-bytes" {
		t.Errorf("Data = %q, want %q", src.Data, "hero-avatar-bytes")
	}
}

func TestAvatarNoRoomAvatarAndNotADMIsANegativeMiss(t *testing.T) {
	state := &avatarFakeState{} // no room avatar, no heroes registered
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	roomID := id.RoomID("!group1:matrix.example.org")
	_, ok, err := a.Avatar(t.Context(), roomID.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v, want nil (a negative miss, not an error)", err)
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false")
	}
}

func TestAvatarDMHeroWithNoAvatarIsANegativeMiss(t *testing.T) {
	state := &avatarFakeState{} // room has no avatar; hero has no avatar either
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	roomID := id.RoomID("!dm2:matrix.example.org")
	a.mu.Lock()
	a.heroes[roomID] = []id.UserID{"@carol:matrix.example.org"}
	a.mu.Unlock()

	_, ok, err := a.Avatar(t.Context(), roomID.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v, want nil (a negative miss, not an error)", err)
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false")
	}
}

func TestAvatarRoomAvatarStateErrorPropagates(t *testing.T) {
	state := &avatarFakeState{roomAvatarErr: http.StatusInternalServerError}
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	_, ok, err := a.Avatar(t.Context(), "!broken:matrix.example.org")
	if err == nil {
		t.Fatal("Avatar() error = nil, want the underlying error propagated")
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false on error")
	}
}

func TestAvatarMediaDownloadErrorPropagates(t *testing.T) {
	state := &avatarFakeState{
		roomAvatarURL: "mxc://matrix.example.org/roompic",
		mediaErr:      http.StatusInternalServerError,
	}
	srv := newAvatarFakeHomeserver(t, state)
	a := newAvatarTestAdapter(t, srv)

	_, ok, err := a.Avatar(t.Context(), "!room1:matrix.example.org")
	if err == nil {
		t.Fatal("Avatar() error = nil, want the download error propagated")
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false on error")
	}
}

var _ core.AvatarProvider = (*Adapter)(nil)
