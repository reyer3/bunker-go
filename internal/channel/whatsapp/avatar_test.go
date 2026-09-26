package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// fakeHTTPGet is an Adapter.httpGet stand-in that records every URL asked
// for and never touches the network.
type fakeHTTPGet struct {
	data map[string][]byte
	err  error
	urls []string
}

func (f *fakeHTTPGet) get(_ context.Context, url string) ([]byte, error) {
	f.urls = append(f.urls, url)
	if f.err != nil {
		return nil, f.err
	}
	return f.data[url], nil
}

func TestAvatarFetchesPreviewProfilePictureForAContact(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicInfo = &types.ProfilePictureInfo{URL: "https://pps.example/pic.jpg", ID: "abc"}
	a := newTestAdapter("personal", cli)

	fetcher := &fakeHTTPGet{data: map[string][]byte{"https://pps.example/pic.jpg": []byte("jpeg-bytes")}}
	a.SetHTTPGet(fetcher.get)

	jid := mustJID(t, "5511999999999@s.whatsapp.net")
	src, ok, err := a.Avatar(context.Background(), jid.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !ok {
		t.Fatal("Avatar() ok = false, want true")
	}
	if string(src.Data) != "jpeg-bytes" {
		t.Errorf("Data = %q, want %q", src.Data, "jpeg-bytes")
	}
	if len(cli.profilePicCalls) != 1 || cli.profilePicCalls[0] != jid {
		t.Fatalf("GetProfilePictureInfo calls = %+v, want one call for %v", cli.profilePicCalls, jid)
	}
	if len(cli.profilePicPreview) != 1 || !cli.profilePicPreview[0] {
		t.Errorf("Preview = %v, want true (avatar fetches must ask for the preview size)", cli.profilePicPreview)
	}
	if len(fetcher.urls) != 1 || fetcher.urls[0] != "https://pps.example/pic.jpg" {
		t.Errorf("httpGet urls = %+v, want the picture URL fetched once", fetcher.urls)
	}
}

func TestAvatarUsesGroupNameForAGroupThread(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicInfo = &types.ProfilePictureInfo{URL: "https://pps.example/group.jpg"}
	names := newFakeNameResolver()
	groupJID := mustJID(t, "12345-67890@g.us")
	names.groupInfo[groupJID] = &types.GroupInfo{GroupName: types.GroupName{Name: "Widget Team"}}

	a := newTestAdapter("personal", cli)
	a.SetNameResolver(names)
	a.SetHTTPGet((&fakeHTTPGet{data: map[string][]byte{"https://pps.example/group.jpg": []byte("png-bytes")}}).get)

	src, ok, err := a.Avatar(context.Background(), groupJID.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v", err)
	}
	if !ok {
		t.Fatal("Avatar() ok = false, want true")
	}
	if src.DisplayName != "Widget Team" {
		t.Errorf("DisplayName = %q, want %q", src.DisplayName, "Widget Team")
	}
}

func TestAvatarNoPictureSetIsANegativeMissNotAnError(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicErr = whatsmeow.ErrProfilePictureNotSet
	a := newTestAdapter("personal", cli)

	jid := mustJID(t, "5511999999999@s.whatsapp.net")
	_, ok, err := a.Avatar(context.Background(), jid.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v, want nil (a negative miss, not an error)", err)
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false (no picture set)")
	}
}

func TestAvatarUnauthorizedIsANegativeMissNotAnError(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicErr = whatsmeow.ErrProfilePictureUnauthorized
	a := newTestAdapter("personal", cli)

	jid := mustJID(t, "5511999999999@s.whatsapp.net")
	_, ok, err := a.Avatar(context.Background(), jid.String())
	if err != nil {
		t.Fatalf("Avatar() error = %v, want nil (a negative miss, not an error)", err)
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false (not authorized)")
	}
}

func TestAvatarOtherProfilePictureErrorPropagates(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicErr = errors.New("network exploded")
	a := newTestAdapter("personal", cli)

	jid := mustJID(t, "5511999999999@s.whatsapp.net")
	_, ok, err := a.Avatar(context.Background(), jid.String())
	if err == nil {
		t.Fatal("Avatar() error = nil, want the underlying error propagated")
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false on error")
	}
}

func TestAvatarInvalidThreadReturnsError(t *testing.T) {
	cli := newFakeWAClient()
	a := newTestAdapter("personal", cli)

	_, ok, err := a.Avatar(context.Background(), "not a jid")
	if err == nil {
		t.Fatal("Avatar() error = nil, want a parse error for an invalid thread")
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false on error")
	}
}

func TestAvatarHTTPFetchErrorPropagates(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicInfo = &types.ProfilePictureInfo{URL: "https://pps.example/pic.jpg"}
	a := newTestAdapter("personal", cli)
	a.SetHTTPGet((&fakeHTTPGet{err: errors.New("timeout")}).get)

	jid := mustJID(t, "5511999999999@s.whatsapp.net")
	_, ok, err := a.Avatar(context.Background(), jid.String())
	if err == nil {
		t.Fatal("Avatar() error = nil, want the HTTP fetch error propagated")
	}
	if ok {
		t.Fatal("Avatar() ok = true, want false on error")
	}
}

var _ core.AvatarProvider = (*Adapter)(nil)

// TestAvatarSkipsNewsletterAndBroadcastWithoutNetwork covers a live hang:
// a newsletter (channel) JID never answered GetProfilePictureInfo and
// the RPC timed out after 30s. Channels and broadcast lists get the
// generated avatar without any network call.
func TestAvatarSkipsNewsletterAndBroadcastWithoutNetwork(t *testing.T) {
	for _, thread := range []string{"120363000000000001@newsletter", "status@broadcast"} {
		cli := newFakeWAClient()
		cli.profilePicHang = true
		a := newTestAdapter("personal", cli)

		_, ok, err := a.Avatar(context.Background(), thread)
		if err != nil || ok {
			t.Errorf("Avatar(%s) = ok %v, err %v; want a negative miss (false, nil)", thread, ok, err)
		}
		if len(cli.profilePicCalls) != 0 {
			t.Errorf("Avatar(%s) made %d GetProfilePictureInfo calls, want 0", thread, len(cli.profilePicCalls))
		}
	}
}

// TestAvatarBoundsAnUnansweredLookup keeps one unanswered lookup from
// holding the RPC until its deadline: the adapter gives up on its own.
func TestAvatarBoundsAnUnansweredLookup(t *testing.T) {
	cli := newFakeWAClient()
	cli.profilePicHang = true
	a := newTestAdapter("personal", cli)
	a.avatarTimeout = 50 * time.Millisecond

	start := time.Now()
	_, _, err := a.Avatar(context.Background(), "5511999999999@s.whatsapp.net")
	if err == nil {
		t.Fatal("Avatar() error = nil, want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Avatar() took %v, want it bounded by avatarTimeout", elapsed)
	}
}
