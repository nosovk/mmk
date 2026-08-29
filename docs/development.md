# Development

## Requirements

- Go version declared by `go.mod` (`1.26.1` at the time of writing)
- Git
- Linux X11 development headers for `golang.design/x/clipboard` when building in environments where cgo is enabled
- A desktop credential store for live onboarding: Secret Service on Linux, Keychain Services on macOS, or Credential Manager on Windows

Mattermost credentials are not needed for unit and integration tests. The test
suite uses fake HTTP servers, fake WebSockets, injectable credential adapters,
and temporary SQLite databases.

## Setup

```bash
git clone https://github.com/nosovk/mmk.git
cd mmk
go mod download
go test ./...
```

Build and run:

```bash
go build -o bin/mmk ./cmd/mmk
./bin/mmk --help
./bin/mmk --add-server
./bin/mmk
```

The Makefile equivalents are `make build`, `make test`, `make lint`, and
`make run`. `make test` runs the full suite with the race detector and verbose
output.

## Required Checks

Run these before opening a pull request:

```bash
go test ./...
go vet ./...
go build ./cmd/mmk
git diff --check
```

The broad race suite used for the Mattermost migration is:

```bash
go test -race \
  ./internal/mattermost/... \
  ./internal/service \
  ./internal/ui \
  ./internal/ui/thread \
  ./cmd/mmk \
  ./internal/image \
  ./internal/export \
  ./internal/emoji
```

Run this race command without another competing Go build or test process. Some
concurrency tests intentionally use short timing bounds and can report misleading
timeouts on a saturated machine.

Useful focused checks:

```bash
go test ./internal/mattermost/...
go test ./internal/service
go test ./cmd/mmk
go test ./internal/ui ./internal/ui/thread
go test ./internal/cache
```

CI additionally runs `go test ./... -race`, Go builds, linting, native
credential-adapter checks on Linux, Windows, and macOS, and a Darwin
`CGO_ENABLED=0` compile check for the explicit Keychain-unavailable fallback.

## Package Layout

| Path | Responsibility |
| --- | --- |
| `cmd/mmk` | CLI parsing, onboarding, production dependency wiring, startup, events, sends, reads, threads, and reconnect reconciliation |
| `internal/mattermost` | URL canonicalization, REST client, WebSocket authentication/events, PAT validation, and OS secret stores |
| `internal/service` | Provider behavior for bootstrap, channel grouping, history, sends, threads, unread derivation, and reconnect policy |
| `internal/cache` | SQLite schema, Mattermost snapshots, memberships, users, posts, and cache-first queries |
| `internal/config` | Optional UI preferences and strict versioned server registry |
| `internal/ui` | Bubble Tea application, reducers, capability gates, panels, compose, history scopes, and thread UI |
| `internal/emoji` | Provider-neutral emoji database, autocomplete, image placement, and width handling |
| `internal/image` | Terminal image detection, fetching, caching, and renderers retained from upstream |
| `internal/notify` | Generic notification helpers retained from upstream but not wired into the Mattermost runtime |
| `internal/export` | Markdown export helpers |
| `docs/plans` | Mattermost migration designs and implementation plans |
| `docs/superpowers` | Historical upstream design records; not authoritative product documentation |

## Architecture Notes

The application is intentionally Mattermost-specific rather than a generic
multi-provider framework:

1. `internal/mattermost` validates wire data and keeps secret-bearing errors redacted.
2. `internal/service` exposes small testable operations over Mattermost data.
3. `internal/cache` provides fast startup and offline fallback but is not the live source of truth.
4. `cmd/mmk` coordinates server-scoped workers and translates service results into Bubble Tea messages.
5. `internal/ui` explicitly disables operations that have no Mattermost production wiring.

The runtime uses canonical Mattermost server roots as stable identity inputs.
Post IDs are opaque strings, timestamps are milliseconds, and all persisted
Mattermost data is server-scoped.

Startup hydrates every available cache before live workers begin. Each server
then authenticates and reconnects independently. Reconciliation updates
authoritative metadata without discarding an active channel's draft, history
scope, selection, thread, or viewport when that channel still exists.

## Testing Guidance

- Prefer fake clients and temporary databases over live credentials.
- Test server scoping explicitly; Mattermost IDs are not assumed globally unique across deployments.
- Cover both cache and live paths for history, startup, and threads.
- For send tests, cover REST-first and WebSocket-first reconciliation orders.
- For async UI tests, assert stale generation results are ignored.
- For credential tests, use injectable native boundaries and verify secrets are redacted and temporary buffers are cleared.
- Do not print, persist, or commit PATs in fixtures, logs, shell commands, or golden files.

## Live Smoke Testing

There is no committed automated smoke-test command that consumes Mattermost
credentials. If you manually test against a real server:

1. Use `mmk --add-server` so the PAT enters through the masked prompt and native credential store.
2. Use a test account and server when possible.
3. Exercise server switching, channel history, send reconciliation, threads, read state, and reconnect behavior.
4. Revoke the test PAT after use if it was temporary.
5. Never add credentials to environment dumps, debug logs, issues, or test output.

## Releases

`.goreleaser.yaml` builds `./cmd/mmk` as `mmk` for Linux and Windows with cgo
disabled, and for macOS with cgo enabled so Keychain Services is available.
Release archives include `README.md` and `LICENSE`; Linux packages are generated
as `.deb`, `.rpm`, and `.apk`.

The tag-triggered workflow runs GoReleaser on macOS. Snapshot validation can be
performed without publishing when GoReleaser is installed:

```bash
goreleaser release --snapshot --clean
```

Publishing the Homebrew cask also requires the repository's
`HOMEBREW_TAP_GITHUB_TOKEN` secret. Do not run a publishing release from a fork
unless its release destinations and credentials have been deliberately changed.
