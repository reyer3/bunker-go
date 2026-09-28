package tui

import (
	"io"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"
	"github.com/reyer3/bunker-go/internal/core"
)

// reactionsEditsGoldenModel builds a ready, opened WhatsApp chat view with
// four fictional messages exercising S2's WhatsApp reactions/edits/revokes
// parity: a plain message, an edited one ("editado" marker), a revoked one
// ("mensaje eliminado", body cleared), and one carrying two reactions from
// different senders — at a pinned clock and a forced TrueColor profile so
// the golden is deterministic regardless of the real wall-clock time or the
// terminal's actual color capability.
func reactionsEditsGoldenModel() Model {
	client := &replyClient{}
	model := chatReadyModel(client, "whatsapp:personal:1")
	model, cmd := openChat(model)
	updated, _ := model.Update(cmd())
	model = updated.(Model)

	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	model.render = r
	model.now = func() time.Time { return at }

	model.chatItems = []core.Item{
		{
			ID: "whatsapp:personal:1", Channel: core.ChannelWhatsApp, Account: "personal",
			Thread: "5511999999999@s.whatsapp.net",
			From:   core.Address{ID: "5511999999999@s.whatsapp.net", Name: "Alice"},
			Body:   "Hola, ¿cómo va todo?", Timestamp: at.Add(-3 * time.Minute),
		},
		{
			ID: "whatsapp:personal:2", Channel: core.ChannelWhatsApp, Account: "personal",
			Thread: "5511999999999@s.whatsapp.net", FromMe: true,
			Body: "Todo bien, corrigiendo la hora", Timestamp: at.Add(-2 * time.Minute),
			Edited: true,
		},
		{
			ID: "whatsapp:personal:3", Channel: core.ChannelWhatsApp, Account: "personal",
			Thread: "5511999999999@s.whatsapp.net",
			From:   core.Address{ID: "5511999999999@s.whatsapp.net", Name: "Alice"},
			Body:   "", Timestamp: at.Add(-1 * time.Minute),
			Deleted: true,
		},
		{
			ID: "whatsapp:personal:4", Channel: core.ChannelWhatsApp, Account: "personal",
			Thread: "5511999999999@s.whatsapp.net", FromMe: true,
			Body: "Nos vemos a las 5", Timestamp: at,
			Reactions: []core.Reaction{
				{Sender: "5511999999999@s.whatsapp.net", Emoji: "👍"},
				{Sender: "personal", Emoji: "😂"},
			},
		},
	}
	return model
}

// TestChatViewRendersEditedRevokedAndReactedMessagesGolden goldens K5's
// chat view showing WhatsApp reaction/edit/revoke parity (S2): an
// "editado" marker under an edited own message, "mensaje eliminado" in
// place of a revoked message's body, and a reactions line under a message
// carrying two reactions.
func TestChatViewRendersEditedRevokedAndReactedMessagesGolden(t *testing.T) {
	m := reactionsEditsGoldenModel()
	teatest.RequireEqualOutput(t, []byte(m.View()))
}
