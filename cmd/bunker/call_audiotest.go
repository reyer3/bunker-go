package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/reyer3/bunker-go/internal/channel/whatsapp"
	"github.com/reyer3/bunker-go/internal/config"
)

const callAudioTestUsage = `usage: bunker call audio-test [--account A] [--seconds N] [--json]

Local audio check, without WhatsApp: plays a short 440 Hz tone through the
account's call playback command and records N seconds (default 3) from its
capture command, then reports the commands used, whether each started, their
stderr, and the microphone level. It runs in this process (not in the
daemon), never touches the network, and so has no --dry-run.`

// Bounds for --seconds: long enough to see a level, short enough to be a
// quick check.
const (
	audioTestDefaultSeconds = 3
	audioTestMaxSeconds     = 30
)

// whatsappAudioTestFunc is the adapter's audio test, a package var so
// tests substitute a fake and never touch a real audio device.
var whatsappAudioTestFunc = whatsapp.AudioTest

// cmdCallAudioTest runs `bunker call audio-test`. It needs only the config
// (for the account's call_* options), not the daemon: the helpers it runs
// are the ones the daemon would run on this same machine.
func cmdCallAudioTest(ctx context.Context, cfg *config.Config, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("call audio-test", stderr)
	account := fs.String("account", "", "WhatsApp account to test (default: the only one)")
	seconds := fs.Int("seconds", audioTestDefaultSeconds, "seconds to record from the microphone")
	jsonOut := fs.Bool("json", false, "emit JSON")
	positionals, err := parseInterspersed(fs, args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(positionals) != 0 {
		fmt.Fprintln(stderr, callAudioTestUsage)
		return 2
	}
	if *seconds < 1 || *seconds > audioTestMaxSeconds {
		return fail(*jsonOut, stdout, stderr, fmt.Errorf("--seconds must be between 1 and %d", audioTestMaxSeconds))
	}
	acc, err := audioTestAccount(cfg, *account)
	if err != nil {
		return fail(*jsonOut, stdout, stderr, err)
	}
	report := whatsappAudioTestFunc(ctx, acc, *seconds)
	if *jsonOut {
		writeJSON(stdout, report)
	} else {
		fmt.Fprint(stdout, report.String())
	}
	if !report.OK {
		return 1
	}
	return 0
}

// audioTestAccount picks the WhatsApp account to test: the named one, or
// the only one configured.
func audioTestAccount(cfg *config.Config, name string) (config.Account, error) {
	if name != "" {
		acc, ok := findAccount(cfg, "whatsapp", name)
		if !ok {
			return config.Account{}, fmt.Errorf("no %q account named %q in config", "whatsapp", name)
		}
		return acc, nil
	}
	var names []string
	var found config.Account
	for _, acc := range cfg.Accounts {
		if acc.Channel == "whatsapp" {
			names = append(names, acc.Name)
			found = acc
		}
	}
	switch len(names) {
	case 0:
		return config.Account{}, fmt.Errorf("no whatsapp account in config")
	case 1:
		return found, nil
	}
	return config.Account{}, fmt.Errorf("several whatsapp accounts (%s): pick one with --account", strings.Join(names, ", "))
}
