module github.com/reyer3/bunker-go

go 1.26.0

toolchain go1.26.8

// Dependency pins. Dependabot (.github/dependabot.yml) groups minor and
// patch updates weekly. The modules below are held on purpose: they are
// excluded from that group, so each arrives as its own PR that needs a
// manual look before merging.
//
//   - github.com/emersion/go-imap/v2 v2.0.0-beta.8: v2 is still beta and
//     each beta has broken the imapclient API. Held until v2.0.0 (or a
//     beta whose changelog shows no breaking change) and a green -race run
//     of internal/channel/mail.
//   - go.mau.fi/whatsmeow (pseudo-version): no tagged releases. WhatsApp is
//     an unofficial client, so bump only to fix a protocol break or a
//     security issue, with the full suite green and the commit log read.
//   - github.com/purpshell/meowcaller (pseudo-version): voice calls on top
//     of whatsmeow; moves only together with whatsmeow.
//   - maunium.net/go/mautrix v0.x: minor releases routinely break the API
//     and the E2EE (goolm) store; bump only when the changelog is read and
//     internal/channel/matrix passes.
//   - github.com/emersion/go-sasl and github.com/charmbracelet/x/exp/teatest
//     (pseudo-versions): no tagged releases; follow go-imap/go-smtp and
//     bubbletea respectively instead of chasing master.

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/charmbracelet/x/ansi v0.11.8
	github.com/charmbracelet/x/exp/teatest v0.0.0-20260924144451-d676b019604b
	github.com/emersion/go-imap/v2 v2.0.0-beta.8
	github.com/emersion/go-message v0.18.2
	github.com/emersion/go-sasl v0.0.0-20241020182733-b788ff22d5a6
	github.com/emersion/go-smtp v0.25.0
	github.com/godbus/dbus/v5 v5.2.2
	github.com/mattn/go-runewidth v0.0.30
	github.com/mdp/qrterminal/v3 v3.2.1
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/muesli/termenv v0.16.0
	github.com/purpshell/meowcaller v0.0.0-20260811012811-27a3c6b18657
	go.mau.fi/util v0.10.1
	go.mau.fi/whatsmeow v0.0.0-20260925162019-b3832c2bd1d1
	golang.org/x/image v0.46.0
	golang.org/x/term v0.46.0
	golang.org/x/text v0.42.0
	maunium.net/go/mautrix v0.31.0
	modernc.org/sqlite v1.60.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/aymanbagabas/go-udiff v0.3.1 // indirect
	github.com/beeper/argo-go v1.1.2 // indirect
	github.com/charmbracelet/colorprofile v0.4.1 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/exp/golden v0.0.0-20241011142426-46044092ad91 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/elliotchance/orderedmap/v3 v3.1.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hajimehoshi/go-mp3 v0.3.4 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/petermattis/goid v0.0.0-20260820044319-269ab09b5261 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.10 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/opus v0.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.9.4 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/pion/transport/v5 v5.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/vektah/gqlparser/v2 v2.5.27 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.mau.fi/libsignal v0.2.2 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/exp v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	rsc.io/qr v0.2.0 // indirect
)
