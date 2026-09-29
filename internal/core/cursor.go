package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// pageCursor is what a Page.NextCursor encodes: the position of the last
// item returned. Listings are ordered by (timestamp DESC, id DESC), and
// the id tiebreak is what keeps paging stable when many items share one
// timestamp (a mail batch synced in one second, a chat backlog restored
// together): a timestamp alone would skip or repeat them.
type pageCursor struct {
	Timestamp int64  `json:"t"`
	ID        string `json:"id"`
}

// EncodeCursor returns the opaque cursor for the item at (ts, id). It is
// base64 so callers treat it as a token, not a format to build by hand.
func EncodeCursor(ts time.Time, id string) string {
	b, _ := json.Marshal(pageCursor{Timestamp: ts.UnixNano(), ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reverses EncodeCursor. A cursor that did not come from
// EncodeCursor is an error, never silently "start from the top", which
// would hand the caller page one again as if it were the next page.
func DecodeCursor(s string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("core: invalid cursor: %w", err)
	}
	var c pageCursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == "" {
		return time.Time{}, "", fmt.Errorf("core: invalid cursor %q", s)
	}
	return time.Unix(0, c.Timestamp).UTC(), c.ID, nil
}
