# bunker

One pure-Go binary that brings your mail, WhatsApp and Matrix into the terminal: a single local store, a status-bar segment for tmux, and a `--json` CLI that a human or an AI assistant (such as Claude Code) can drive safely.

```
bunker daemon (one process)
 ├─ adapters: mail (IMAP IDLE + SMTP) · WhatsApp (whatsmeow) · Matrix (mautrix-go, E2EE)
 ├─ core: one Item model, small capability ports, dry-run gate, fan-out
 └─ store: SQLite (pure Go)
bunker <command> ──unix socket──> daemon
```

## Features

- **Mail**: IMAP IDLE push, full-body fetch without marking read, send and reply with multiple recipients, Cc and attachments, move to folders, labels (IMAP keywords and Gmail labels, read back into the store), and reconciliation with changes made from other clients.
- **WhatsApp**: linked device through QR, contact and group names, bounded history import, text and media (images, video, audio, documents), status posts, read receipts, human-paced sends with presence and typing emulation, and opt-in voice calls (place, answer, reject, hang up) through the daemon machine's microphone and speaker.
- **Matrix**: SSO login, end-to-end encryption in pure Go (goolm), recovery-key and key-export import, re-decryption of old events, typing notifications, read receipts, and encrypted media.
- **Safety by design**:
  - every outbound action has `--dry-run`, which returns a plan and never touches the network;
  - broadcasts on chat channels are capped and paced;
  - an attachment or recipient a channel cannot deliver is an error, never silently dropped.
- **tmux**: `bunker render` prints `✉ 3  💬 5  ⌘ 2` within a 200 ms budget, even when the daemon is down.

## Install

Download a release from the [Releases](https://github.com/reyer3/bunker-go/releases) page, or build it yourself:

```sh
CGO_ENABLED=0 go install -tags goolm github.com/reyer3/bunker-go/cmd/bunker@latest
```

The `goolm` build tag is required: it selects the pure-Go Olm implementation for Matrix E2EE.

## Quick start

```sh
mkdir -p ~/.config/bunker-go
cp docs/config.example.toml ~/.config/bunker-go/config.toml   # edit your accounts
bunker link whatsapp personal          # scan the QR code
bunker link matrix work                # SSO in the browser
bunker daemon                          # or install deploy/systemd/bunker.service
bunker list --unread
bunker send mail work "a@example.com,b@example.com" - --subject "Hi" --attach report.pdf --dry-run
```

Try it without any account: `bunker daemon --fake`.

The full command reference and JSON shapes are in [docs/cli.md](docs/cli.md).

## Development

```sh
make test   # go test -tags goolm ./...
make race
make vet
```

Tests never touch real servers. They use in-process fakes for IMAP, SMTP, the Matrix homeserver and the WhatsApp client.

## Notes

- WhatsApp support uses [whatsmeow](https://github.com/tulir/whatsmeow), an unofficial client. Voice calls add [meowcaller](https://github.com/purpshell/meowcaller), an experimental, unofficial VoIP implementation; they are off unless an account sets `calls = true`. Automated or bulk messaging can get an account banned, so keep usage human-paced; the built-in limits exist for that reason.
- Secrets never go into the config file. Mail uses GNOME Online Accounts or a 0600 password file; WhatsApp and Matrix sessions live in a 0700 state directory.

## License

Apache-2.0. See [LICENSE](LICENSE).
