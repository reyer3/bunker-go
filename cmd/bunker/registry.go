package main

import (
	"fmt"

	"github.com/reyer3/bunker-go/internal/channel/fake"
	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
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

// demoAdapter builds a fake.Adapter preloaded with one demo item, used by
// "bunker daemon --fake".
func demoAdapter(channel core.Channel, account string) *fake.Adapter {
	item := core.Item{
		ID:      fmt.Sprintf("%s:%s:1", channel, account),
		Channel: channel,
		Account: account,
		Subject: fmt.Sprintf("Demo %s item", channel),
		Body:    fmt.Sprintf("This is a fake %s message from bunker-go's demo mode.", channel),
		From:    core.Address{ID: "demo", Name: "bunker-go demo"},
		Unread:  true,
	}
	return fake.New(channel, account, item)
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
