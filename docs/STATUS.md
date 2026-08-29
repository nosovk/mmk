# mmk Implementation Status

Last updated: 2026-08-29

## Product State

`mmk` is a Mattermost-only terminal client. Slack authentication, clients,
protocols, runtime wiring, models, formatting adapters, and dependencies were
removed in Task 15. Historical design documents may still discuss the upstream
Slack implementation; they are not descriptions of the current product.

The Mattermost MVP is an end-to-end vertical slice for reading and sending text
messages across multiple servers and teams, including realtime delivery,
offline cache use, unread state, reconnect reconciliation, and thread replies.

## Completed Migration

- [x] Tasks 1-4: project identity, Mattermost domain models, REST foundation, teams, channels, memberships, and users
- [x] Task 5: interactive `mmk --add-server`, PAT validation, versioned server registry, and native credential-store adapters
- [x] Tasks 6-8: cache-first multi-server startup, server rail, team-grouped sidebar, switching, and provider capability gates
- [x] Task 9: exact Mattermost history ordering, opaque post IDs, author resolution, cached startup, and older pagination
- [x] Task 10: channel and thread post creation with optimistic delivery and correlation reconciliation
- [x] Task 11: authenticated WebSocket transport and validated `posted` event delivery
- [x] Task 12: independent reconnect loops, metadata reconciliation, active-channel catch-up, cache fallback, and realtime persistence
- [x] Task 13: unread derivation, optimistic channel-read transitions, REST channel-view synchronization, and realtime corrections
- [x] Task 14: cache-first thread panels, thread fetch, reply sends, and realtime reply routing
- [x] Task 15: Slack runtime and dependency removal, Mattermost plain-text rendering, and provider-neutral emoji autocomplete
- [x] Task 16: Mattermost documentation and end-to-end verification

## Supported Mattermost MVP

### Setup And Servers

- Interactive PAT onboarding through `mmk --add-server`
- Server URL canonicalization, including deployment subpaths and optional `/api/v4` suffixes
- Credential validation against the current-user and team APIs before persistence
- PAT storage in Linux Secret Service, macOS Keychain Services, or Windows Credential Manager
- Non-secret, versioned `servers.toml` registry with atomic replacement and owner-only permissions for new files
- Updating an existing server by repeating onboarding with the same canonical URL
- Multiple configured servers connected and reconnected independently
- All teams available to the authenticated user grouped beneath their server

### Channels And Messages

- Public, private, direct, and group-message channels from current memberships
- Team-grouped sidebar and server rail with unread presentation
- Cache hydration before live startup, with cached navigation when a server is offline
- Recent channel history and older-history pagination
- Mattermost post IDs and millisecond timestamps throughout the active runtime
- Text rendering with wrapping, day separators, author display names, broadcast mentions, and literal Mattermost emoji shortcodes
- Local emoji autocomplete while composing
- Channel message sends with pending, sent, and failed delivery states
- Correlation-based reconciliation whether the REST response or WebSocket event arrives first
- Realtime new posts persisted before presentation
- Optimistic read clearing followed by Mattermost channel-view synchronization

### Threads And Resilience

- Open a root post in the side thread panel
- Cache-first thread root and reply rendering
- Fetch authoritative thread contents and enrich missing authors
- Send replies using Mattermost root post IDs
- Route realtime replies into open thread scopes
- Independent authenticated WebSocket reconnect with capped jittered exponential backoff
- Non-destructive server metadata reconciliation after reconnect
- Active-channel recent-history catch-up after reconnect
- Cached history retained and shown when a live history request fails
- Stale async results dropped by server, channel, selection, and window generations

### Terminal UI

- Keyboard-first modal navigation and multi-line compose
- Local fuzzy channel finder
- Multiple server switching through the rail and digit keys
- Built-in and custom themes
- Mouse scrolling and terminal unread title updates
- SQLite-backed scrollback and render caching

## Current Limitations

The retained UI still contains generic components and cache tables for features
that existed upstream. A component's presence does not mean the Mattermost
runtime wires the corresponding server operation.

The following are deliberately disabled or deferred for Mattermost:

- Reactions and reaction synchronization
- File uploads, attachment downloads, and inline Mattermost attachments
- Message editing and deletion
- Message search and remote channel search
- Status, presence, and do-not-disturb controls
- Typing indicators
- Desktop notifications
- Starting a new DM or group message from `mmk`
- Copying Mattermost permalinks
- Mark-unread
- Workspace-wide Threads/involved-threads view and thread subscriptions
- Custom Mattermost emoji discovery; autocomplete currently uses the bundled provider-neutral emoji set

Additional operational limitations:

- PATs provide full access to the associated account; `mmk` does not implement OAuth or password login.
- PAT creation must be enabled by the Mattermost administrator.
- Linux requires an available, unlocked Secret Service implementation; macOS release builds require cgo for Keychain Services.
- The app has no plaintext credential fallback and cannot start a configured server if its PAT cannot be read.
- There is no interactive remove-server command. Registry or credential cleanup currently requires manual administrator/user action.
- `config.toml` retains a broad upstream schema, but the current Mattermost startup wires only global theme selection, custom theme colors, and mouse-wheel speed. See `docs/configuration.md`.

## Architecture

```text
mmk/
├── cmd/mmk/                  # CLI, onboarding, runtime wiring, event/reconnect adapters
├── internal/cache/           # SQLite migrations and Mattermost snapshots/history
├── internal/config/          # config.toml and versioned servers.toml handling
├── internal/mattermost/      # REST, WebSocket, PAT validation, native secret stores
├── internal/service/         # Bootstrap, channels, history, sends, threads, reconnect policy
├── internal/ui/              # Bubble Tea application and capability-gated components
├── internal/emoji/           # Provider-neutral emoji data and terminal rendering helpers
├── internal/image/           # Retained terminal image infrastructure
└── docs/                     # Current guides plus historical plans and designs
```

Key boundaries:

1. `internal/mattermost` owns transport validation and redaction.
2. `internal/service` converts API data into server, channel, history, send, and thread behavior.
3. `internal/cache` stores server-scoped snapshots and posts for cache-first startup.
4. `cmd/mmk` owns production dependency wiring and generation-scoped reconciliation.
5. `internal/ui` remains provider-aware through explicit Mattermost capability gates rather than pretending deferred operations are available.

## Verification Baseline

Task 16's release-quality baseline is:

```bash
go test ./...
go vet ./...
go build ./cmd/mmk
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

See [development.md](development.md) for developer setup and focused checks.
