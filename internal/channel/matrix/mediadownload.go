package matrix

import (
	"context"
	"errors"
	"io"
	"net/http"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// downloadMedia fetches uri's bytes from the homeserver's media repo.
// mautrix's DownloadBytes only speaks authenticated media
// (GET /_matrix/client/v1/media/download, spec v1.11); a homeserver that
// predates it answers M_UNRECOGNIZED, so this retries once on the legacy
// GET /_matrix/media/v3/download path (still sending the access token)
// and, when that works, remembers it for the adapter's later downloads.
// Any other error (M_FORBIDDEN, M_NOT_FOUND, ...) is returned unchanged.
func (a *Adapter) downloadMedia(ctx context.Context, uri id.ContentURI) ([]byte, error) {
	if a.legacyMedia.Load() {
		return a.downloadLegacyMedia(ctx, uri)
	}
	data, err := a.client.DownloadBytes(ctx, uri)
	if err == nil || !errors.Is(err, mautrix.MUnrecognized) {
		return data, err
	}
	data, legacyErr := a.downloadLegacyMedia(ctx, uri)
	if legacyErr != nil {
		return nil, errors.Join(err, legacyErr)
	}
	a.legacyMedia.Store(true)
	return data, nil
}

// downloadLegacyMedia is DownloadBytes against the pre-v1.11 media path.
func (a *Adapter) downloadLegacyMedia(ctx context.Context, uri id.ContentURI) ([]byte, error) {
	_, resp, err := a.client.MakeFullRequestWithResp(ctx, mautrix.FullRequest{
		Method:           http.MethodGet,
		URL:              a.client.BuildURL(mautrix.MediaURLPath{"v3", "download", uri.Homeserver, uri.FileID}),
		DontReadResponse: true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
