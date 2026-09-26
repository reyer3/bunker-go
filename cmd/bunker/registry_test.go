package main

import (
	"testing"

	"github.com/reyer3/bunker-go/internal/config"
	"github.com/reyer3/bunker-go/internal/core"
)

func TestBuildRegistryUsesRegisteredConstructor(t *testing.T) {
	orig := adapterConstructors
	adapterConstructors = map[string]adapterConstructor{}
	defer func() { adapterConstructors = orig }()

	RegisterAdapter("mail", func(acc config.Account) (core.Adapter, error) {
		return demoAdapter(core.ChannelMail, acc.Name), nil
	})

	cfg := &config.Config{Accounts: []config.Account{{Channel: "mail", Name: "cl"}}}
	reg, err := buildRegistry(cfg)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	if _, ok := reg.Get(core.ChannelMail, "cl"); !ok {
		t.Fatal("expected mail/cl adapter to be registered")
	}
}

func TestBuildRegistryUnknownChannelErrors(t *testing.T) {
	orig := adapterConstructors
	adapterConstructors = map[string]adapterConstructor{}
	defer func() { adapterConstructors = orig }()

	cfg := &config.Config{Accounts: []config.Account{{Channel: "matrix", Name: "work"}}}
	_, err := buildRegistry(cfg)
	if err == nil {
		t.Fatal("expected error for an unregistered channel")
	}
}

func TestDemoRegistryRegistersAllThreeChannels(t *testing.T) {
	reg := demoRegistry()
	for _, ch := range []core.Channel{core.ChannelMail, core.ChannelWhatsApp, core.ChannelMatrix} {
		if _, ok := reg.Get(ch, "demo"); !ok {
			t.Fatalf("expected demo/%s adapter to be registered", ch)
		}
	}
}
