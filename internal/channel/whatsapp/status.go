package whatsapp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/reyer3/bunker-go/internal/core"
)

// parseBackgroundARGB turns a "#RRGGBB" or "0xAARRGGBB" background string
// into whatsmeow's ARGB uint32. A bare "#RRGGBB" gets full alpha. Empty or
// unparseable input returns nil, meaning "let WhatsApp choose".
func ptrUint64(v uint64) *uint64 { return &v }

func parseBackgroundARGB(s string) *uint32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	hasAlpha := false
	switch {
	case strings.HasPrefix(s, "0x"), strings.HasPrefix(s, "0X"):
		s = s[2:]
		hasAlpha = len(s) == 8
	case strings.HasPrefix(s, "#"):
		s = s[1:]
	}
	if !hasAlpha && len(s) != 6 {
		if len(s) != 8 {
			return nil
		}
		hasAlpha = true
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return nil
	}
	argb := uint32(v)
	if !hasAlpha {
		argb |= 0xFF000000
	}
	return &argb
}

// PostStatus publishes a text or image status/story to every contact
// (types.StatusBroadcastJID). whatsmeow marks status sending as
// experimental; this construction is covered by tests here but not
// exercised against a real account (see the feature doc's constraints).
func (a *Adapter) PostStatus(ctx context.Context, status core.Status) (core.Receipt, error) {
	var msg *waE2E.Message
	if status.Media == "" {
		msg = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:           ptrString(status.Text),
				BackgroundArgb: parseBackgroundARGB(status.Background),
				Font:           waE2E.ExtendedTextMessage_SYSTEM.Enum(),
			},
		}
	} else {
		built, err := a.buildImageMessage(ctx, status.Media, status.Text)
		if err != nil {
			return core.Receipt{}, fmt.Errorf("whatsapp: status: %w", err)
		}
		msg = built
	}

	resp, err := a.cli.SendMessage(ctx, types.StatusBroadcastJID, msg)
	if err != nil {
		return core.Receipt{}, fmt.Errorf("whatsapp: status: send: %w", err)
	}
	return core.Receipt{
		ID:      itemID(a.account, types.StatusBroadcastJID.String(), string(resp.ID)),
		Channel: core.ChannelWhatsApp,
		At:      resp.Timestamp,
	}, nil
}
