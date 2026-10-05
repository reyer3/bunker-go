package main

import (
	"context"
	"fmt"
	"time"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
	"github.com/reyer3/bunker-go/internal/oggfixture"
)

// adapterConstructor builds a core.Adapter for one configured account.
type adapterConstructor func(acc config.Account) (core.Adapter, error)

// adapterConstructors maps a config account's channel name to the
// function that builds its adapter. cmd/bunker never imports adapter
// packages directly: each L2 channel package (mail, whatsapp, matrix)
// calls RegisterAdapter from its own init() to join the CLI's wiring.
// Only "fake" demo data is registered in this task; T2-T4 add the rest.
var adapterConstructors = map[string]adapterConstructor{}

// RegisterAdapter is the plug-in point L2 channel packages use.
func RegisterAdapter(channel string, ctor adapterConstructor) {
	adapterConstructors[channel] = ctor
}

// buildRegistry constructs one adapter per configured account, using
// whichever channel package has registered itself for that account's
// channel.
func buildRegistry(cfg *config.Config) (*core.Registry, error) {
	reg := core.NewRegistry()
	for _, acc := range cfg.Accounts {
		ctor, ok := adapterConstructors[acc.Channel]
		if !ok {
			return nil, fmt.Errorf("cmd/bunker: no adapter registered for channel %q (account %q)", acc.Channel, acc.Name)
		}
		adapter, err := ctor(acc)
		if err != nil {
			return nil, fmt.Errorf("cmd/bunker: build adapter %s/%s: %w", acc.Channel, acc.Name, err)
		}
		reg.Register(adapter)
	}
	return reg, nil
}

// demoAdapter builds a fake.Adapter preloaded with one unread demo item
// (plus, on the chat channels, a few already-read conversations so the
// WhatsApp and Matrix tabs show a chat list), used by "bunker daemon
// --fake".
func demoAdapter(channel core.Channel, account string) *fake.Adapter {
	now := time.Now()
	item := core.Item{
		ID:      fmt.Sprintf("%s:%s:1", channel, account),
		Channel: channel,
		Account: account,
		// Chats open by thread: without one the demo chat would open empty.
		Thread:     "demo",
		ThreadName: fmt.Sprintf("Demo %s", channel),
		Subject:    fmt.Sprintf("Demo %s item", channel),
		Body:       fmt.Sprintf("This is a fake %s message from bunker-go's demo mode.", channel),
		From:       core.Address{ID: "demo", Name: "bunker-go demo"},
		Unread:     true,
		Timestamp:  now.Add(-5 * time.Minute),
	}
	seed := []core.Item{item}
	if channel == core.ChannelWhatsApp || channel == core.ChannelMatrix {
		for i, c := range []struct{ thread, name, from, body string }{
			{"demo-ana", "Demo Ana", "Demo Ana", "Nos vemos mañana a las 10."},
			{"demo-team", "Demo equipo", "Demo Luis", "Subí las notas de la reunión."},
		} {
			seed = append(seed, core.Item{
				ID:         fmt.Sprintf("%s:%s:chat%d", channel, account, i+1),
				Channel:    channel,
				Account:    account,
				Thread:     c.thread,
				ThreadName: c.name,
				Body:       c.body,
				From:       core.Address{ID: c.thread, Name: c.from},
				Timestamp:  now.Add(time.Duration(-(i + 1)) * time.Hour),
			})
		}
	}
	switch channel {
	case core.ChannelMail:
		seed = append(seed, demoInvitations(account, now)...)
	case core.ChannelWhatsApp:
		// A call link shared in a chat, for the "Reuniones" section's
		// link entries (no time, only listed for a day).
		seed = append(seed, core.Item{
			ID: fmt.Sprintf("%s:%s:meet1", channel, account), Channel: channel, Account: account,
			Thread: "demo-team", ThreadName: "Demo equipo",
			Body:      "Sala abierta para el repaso: https://meet.jit.si/bunker-demo",
			From:      core.Address{ID: "demo-team", Name: "Demo Luis"},
			Timestamp: now.Add(-10 * time.Minute),
		})
	}
	demoVoice := oggfixture.Bytes(12 * time.Second)
	voiceID := fmt.Sprintf("%s:%s:voice1", channel, account)
	if channel == core.ChannelWhatsApp || channel == core.ChannelMatrix {
		// A voice note in the first demo chat, with a valid (silent) file
		// behind it, so the bubble and its playback can be tried without
		// real accounts.
		seed = append(seed, core.Item{
			ID: voiceID, Channel: channel, Account: account,
			Thread: "demo-ana", ThreadName: "Demo Ana",
			From:      core.Address{ID: "demo-ana", Name: "Demo Ana"},
			Timestamp: now.Add(-30 * time.Minute),
			Attachments: []core.Attachment{{
				Name: "audio", MIME: "audio/ogg; codecs=opus", Size: int64(len(demoVoice)), Ref: "demo",
				Voice: true, Duration: 12, Waveform: []byte{10, 30, 60, 90, 70, 40, 20, 50, 80, 100, 60, 30, 15, 45, 75, 55, 25, 10},
			}},
		})
	}
	a := fake.New(channel, account, seed...)
	if channel == core.ChannelWhatsApp || channel == core.ChannelMatrix {
		a.SetAttachmentData(voiceID, 0, demoVoice)
	}
	return a
}

// demoInvitations are two already-read calendar invitations, so the
// "Reuniones" section can be tried without real accounts: one starting in
// 25 minutes (with a Meet link) and one tomorrow at 10:00 (Zoom).
func demoInvitations(account string, now time.Time) []core.Item {
	soon := now.Add(25 * time.Minute).Truncate(time.Minute)
	day := now.AddDate(0, 0, 1)
	tomorrow := time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, now.Location())
	var items []core.Item
	for i, m := range []core.Meeting{
		{UID: "demo-weekly@bunker.invalid", Method: "REQUEST", Summary: "Revisión semanal",
			Start: soon, End: soon.Add(time.Hour), Organizer: "Demo Ana <ana@example.com>",
			URL: "https://meet.google.com/abc-defg-hij"},
		{UID: "demo-product@bunker.invalid", Method: "REQUEST", Summary: "Demo de producto",
			Start: tomorrow, End: tomorrow.Add(45 * time.Minute), Organizer: "Demo Luis <luis@example.com>",
			URL: "https://us02web.zoom.us/j/123456789"},
	} {
		at := now.Add(time.Duration(-(i + 1)) * time.Hour)
		meta, err := core.MeetingMeta(map[string]string{"folder": "INBOX"}, m, at)
		if err != nil {
			continue // a demo meeting that cannot be encoded is simply not shown
		}
		items = append(items, core.Item{
			ID: fmt.Sprintf("mail:%s:invite%d", account, i+1), Channel: core.ChannelMail, Account: account,
			Thread: fmt.Sprintf("invite%d", i+1), ThreadName: "Invitación: " + m.Summary,
			Subject:   "Invitación: " + m.Summary,
			Body:      "Te invitamos a " + m.Summary + ".",
			From:      core.Address{ID: "ana@example.com", Name: "Demo Ana"},
			Timestamp: at, Meta: meta,
		})
	}
	return items
}

// demoTodos are the to-dos "bunker daemon --fake" starts with, one each
// way, linked to the demo chats. Their fixed ids make seeding them again
// on every start a no-op (AddTodo is idempotent by id).
func demoTodos(now time.Time) []core.Todo {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return []core.Todo{
		{ID: "demo1", Text: "Mandar las notas de la reunión", Direction: core.TodoMine, Status: core.TodoOpen,
			Due: today.AddDate(0, 0, 1), ItemID: "whatsapp:demo:chat1", Channel: core.ChannelWhatsApp,
			Account: "demo", Thread: "demo-ana", Person: "Demo Ana", Created: now.Add(-time.Hour)},
		{ID: "demo2", Text: "Confirmar la sala para el repaso", Direction: core.TodoTheirs, Status: core.TodoOpen,
			ItemID: "matrix:demo:chat2", Channel: core.ChannelMatrix, Account: "demo", Thread: "demo-team",
			Person: "Demo Luis", Created: now.Add(-2 * time.Hour)},
	}
}

// seedDemoTodos stores demoTodos straight into the store: through
// Service.AddTodo they would need their chat items stored first, and the
// fake adapters only push those once they run.
func seedDemoTodos(ctx context.Context, st core.TodoStore, now time.Time) error {
	for _, t := range demoTodos(now) {
		if _, err := st.AddTodo(ctx, t); err != nil {
			return fmt.Errorf("daemon: seed demo to-dos: %w", err)
		}
	}
	return nil
}

// demoRegistry wires one fake adapter per channel under the "demo"
// account, for "bunker daemon --fake".
func demoRegistry() *core.Registry {
	reg := core.NewRegistry()
	reg.Register(demoAdapter(core.ChannelMail, "demo"))
	reg.Register(demoAdapter(core.ChannelWhatsApp, "demo"))
	reg.Register(demoAdapter(core.ChannelMatrix, "demo"))
	return reg
}
