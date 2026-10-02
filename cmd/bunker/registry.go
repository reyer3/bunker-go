package main

import (
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

// demoRegistry wires one fake adapter per channel under the "demo"
// account, for "bunker daemon --fake".
func demoRegistry() *core.Registry {
	reg := core.NewRegistry()
	reg.Register(demoAdapter(core.ChannelMail, "demo"))
	reg.Register(demoAdapter(core.ChannelWhatsApp, "demo"))
	reg.Register(demoAdapter(core.ChannelMatrix, "demo"))
	return reg
}
