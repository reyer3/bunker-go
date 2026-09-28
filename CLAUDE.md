# bunker-go

One pure-Go binary: a daemon that runs mail (IMAP/SMTP), WhatsApp
(whatsmeow) and Matrix (mautrix-go, E2EE) adapters into one SQLite store,
and a CLI/TUI that drives it over a unix socket. The public repo is the
only repo: everything committed here is published.

## Layout

- `cmd/bunker/`: the binary. `main.go` dispatches, `commands.go` and
  `call.go` hold CLI commands, `wire.go` registers adapters, `backend.go`
  is the `Backend` interface commands run against (the RPC client, or an
  in-process `core.Service`).
- `internal/core/`: the channel-agnostic model. `types.go` (`Item`),
  `ports.go` (the `Adapter` interface plus optional capabilities such as
  `Sender`, `Organizer`, `ReadMarker`), `service.go` (`Service`, which
  every command goes through), `call.go` (voice calls).
- `internal/channel/{mail,whatsapp,matrix,fake}/`: one adapter per channel,
  each implementing only the capabilities it supports.
- `internal/rpc/`: line-delimited JSON over the unix socket
  (`protocol.go` methods, `server.go`, `client.go`).
- `internal/store/` (SQLite), `internal/tui/` (bubbletea UI),
  `internal/config/` (TOML accounts).
- `docs/cli.md` is the CLI reference and JSON shapes;
  `docs/config.example.toml` documents every account option.

## Adding a capability end to end

Follow an existing one (e.g. `StatusPublisher` or `Caller`):
1. an optional interface in `internal/core` and a `Service` method that
   type-asserts it, returning an error wrapping `core.ErrUnsupported` when
   the adapter lacks it;
2. the adapter implementation behind a narrow interface over the
   third-party client, so tests use a fake;
3. an RPC method in `protocol.go`/`server.go`/`client.go`;
4. a `Backend` method, the CLI command, `fakeBackend` in
   `cmd/bunker/backend_test.go`, and `topLevelUsage`;
5. `docs/cli.md` and, for new options, `docs/config.example.toml`.

## Rules

- **Pure Go, no CGO.** `CGO_ENABLED=0 go build -tags goolm ./...` must
  pass. The `goolm` tag is always required. Talk to native things (audio,
  video) through external processes, not bindings.
- **Every outbound action supports `--dry-run`**, which returns a `Plan`
  and never touches the network.
- **Human pacing on chat channels.** WhatsApp is an unofficial client:
  keep the human-emulation and fan-out limits; never add bulk or automated
  sending. Risky features (e.g. voice calls) are opt-in per account.
- **Fail loudly.** An attachment, recipient or action a channel cannot
  deliver is an error, never silently dropped.
- **Tests never touch real servers.** Use the in-process fakes.
- **Nothing sensitive in the repo.** No real names, numbers, domains,
  account IDs or secrets in code, tests, docs or commit messages. CI and
  the pre-commit hook check a private denylist (`scripts/check-denylist.sh`).
- Match the surrounding style: comments explain why, errors are wrapped
  with a `package: ` prefix, and user-facing TUI and item text is in
  Spanish.

## Commands

```sh
make test      # go test -tags goolm ./...
make race      # with -race (what CI runs)
make vet
make build     # CGO_ENABLED=0 build of every package
make dev       # install to ~/.local/bin and restart the daemon service
make hooks     # install the pre-commit denylist check
bunker daemon --fake   # a demo daemon with no real accounts
```

CI (`.github/workflows/ci.yml`) also runs gofmt, staticcheck, govulncheck,
betterleaks and the denylist check.

## Commits and releases

Use conventional commits (`feat:`, `fix:`, `docs:`, `test:`, `build:`,
`chore:`): release-please turns them into the next version and the
changelog. Merging the release PR it maintains publishes the release
(`.github/workflows/release.yml`, which needs the `RELEASE_PLEASE_TOKEN`
secret). Never tag or bump versions by hand.
