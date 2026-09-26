package main

import "testing"

// TestChannelConstructorsAreRegisteredAtStartup proves cmd/bunker's own
// init() wires every L2 adapter's constructor into adapterConstructors,
// so a real config.toml with mail/whatsapp/matrix accounts builds a real
// registry, not just the fake channel exercised elsewhere.
func TestChannelConstructorsAreRegisteredAtStartup(t *testing.T) {
	for _, channel := range []string{"mail", "whatsapp", "matrix"} {
		if _, ok := adapterConstructors[channel]; !ok {
			t.Errorf("expected channel %q to have a registered constructor at startup", channel)
		}
	}
}
