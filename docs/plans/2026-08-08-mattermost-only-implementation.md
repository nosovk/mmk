# mmk Mattermost-Only Fork Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Convert the `gammons/slk` fork into `mmk`, a Mattermost-only TUI supporting multiple servers and teams, cached chat, realtime messages, unread state, and threads.

**Architecture:** Preserve the Bubble Tea UI and reusable terminal infrastructure while replacing Slack authentication, transport, formatting, and domain objects with compact Mattermost-native models. Mattermost REST API v4 is authoritative, WebSocket events provide realtime deltas, and SQLite remains a cache.

**Tech Stack:** Go 1.26, Bubble Tea v2, Lip Gloss v2, Gorilla WebSocket, SQLite, TOML, OS credential storage, `net/http`, `httptest`.

---

### Task 1: Rename Project Identity

**Files:**
- Modify: `go.mod`
- Move: `cmd/slk/` to `cmd/mmk/`
- Modify: `cmd/mmk/main.go`
- Modify: `Makefile`
- Modify: `.goreleaser.yaml`
- Modify: `internal/config/paths.go`
- Modify: `internal/version/version.go`
- Modify: `README.md`
- Test: `cmd/mmk/main_test.go`

**Step 1: Write a failing identity test**

Add a test around a small application identity function or constant that expects the executable name, config directory, and user-facing name to be `mmk`.

**Step 2: Run the focused test and verify RED**

Run: `go test ./cmd/mmk -run TestApplicationIdentity -v`

Expected: FAIL because the package/path or identity still names `slk`.

**Step 3: Apply the minimal rename**

Rename the Go module to a neutral local module path chosen for this repository, rename `cmd/slk` to `cmd/mmk`, and update import paths mechanically. Change build outputs and config/cache directories to `mmk`. Keep the upstream LICENSE unchanged and add derived-project attribution to README.

**Step 4: Verify GREEN and regression suite**

Run: `go test ./cmd/mmk -run TestApplicationIdentity -v`

Expected: PASS.

Run: `go test ./...`

Expected: PASS.

**Step 5: Commit**

```bash
git add go.mod go.sum cmd internal Makefile .goreleaser.yaml README.md
git commit -m "chore: rename slk fork to mmk"
```

### Task 2: Introduce Mattermost Domain Models

**Files:**
- Create: `internal/mattermost/models.go`
- Create: `internal/mattermost/models_test.go`

**Step 1: Write failing model behavior tests**

Cover channel-kind decoding, direct-message detection, thread-root handling, and display-name precedence for users.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost -run 'Test(ChannelKind|MessageThreadRoot|UserDisplayName)' -v`

Expected: FAIL because the package and model helpers do not exist.

**Step 3: Implement minimal compact models**

Add `Server`, `Team`, `User`, `Channel`, `Message`, `ChannelKind`, and connection-state types. Avoid importing Slack types or the complete Mattermost server model package.

**Step 4: Verify GREEN**

Run: `go test ./internal/mattermost -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost
git commit -m "feat: add Mattermost domain models"
```

### Task 3: Add REST Client Foundation

**Files:**
- Create: `internal/mattermost/client.go`
- Create: `internal/mattermost/client_test.go`
- Create: `internal/mattermost/errors.go`

**Step 1: Write failing tests**

Use `httptest.Server` to assert:

- base URLs normalize to a single `/api/v4` prefix;
- requests send `Authorization: Bearer <token>`;
- JSON responses decode into application models;
- Mattermost error payloads retain status, ID, and message.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost -run 'TestClient|TestAPIError' -v`

Expected: FAIL because `Client` does not exist.

**Step 3: Implement minimal client**

Implement `NewClient`, a private request helper, and `CurrentUser`. Use `net/http`; do not add a broad SDK dependency yet.

**Step 4: Verify GREEN**

Run: `go test ./internal/mattermost -run 'TestClient|TestAPIError' -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost/client.go internal/mattermost/client_test.go internal/mattermost/errors.go
git commit -m "feat: add authenticated Mattermost REST client"
```

### Task 4: Fetch Teams And Channels

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`

**Step 1: Write failing endpoint tests**

Cover `TeamsForUser`, truthful `ChannelsForUser(ctx, userID string)` loading the complete cross-team response without pagination query parameters, and channel membership metadata including `last_viewed_at` and mention counts.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost -run 'TestClient_(TeamsForUser|ChannelsForUser)' -v`

Expected: FAIL with missing methods.

**Step 3: Implement endpoint methods and boundary conversion**

Decode only fields needed by `mmk`. Stream the unpaginated cross-team channel array under a finite 64 MiB limit, and classify open, private, direct, and group channels at the boundary.

**Step 4: Verify GREEN**

Run: `go test ./internal/mattermost -run 'TestClient_(TeamsForUser|ChannelsForUser)' -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost/client.go internal/mattermost/client_test.go
git commit -m "feat: load Mattermost teams and channels"
```

### Task 5: Add PAT Secret Storage And Server Configuration

**Files:**
- Create: `internal/mattermost/auth.go`
- Create: `internal/mattermost/auth_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/mmk/onboarding.go`
- Create: `cmd/mmk/onboarding_mattermost_test.go`

**Step 1: Write failing tests**

Test URL normalization, server config persistence without tokens, credential-store key naming, validation through `/users/me`, and failure when secret storage is unavailable.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./internal/config ./cmd/mmk -v`

Expected: FAIL because the Mattermost onboarding path does not exist.

**Step 3: Implement minimal onboarding core**

Create testable non-interactive onboarding logic taking a secret-store interface. Wire `mmk --add-server` to the existing form library. Store URL and display metadata in TOML and PAT only in secure storage.

**Step 4: Verify GREEN**

Run the complete Task 5 package command again and expect PASS.

**Step 5: Commit**

```bash
git add internal/mattermost/auth.go internal/mattermost/auth_test.go internal/config cmd/mmk/onboarding.go cmd/mmk/onboarding_mattermost_test.go
git commit -m "feat: add Mattermost server onboarding"
```

### Task 6: Build Server Bootstrap Service

**Files:**
- Create: `internal/service/server.go`
- Create: `internal/service/server_test.go`
- Create: `internal/service/channels.go`
- Create: `internal/service/channels_test.go`

**Step 1: Write failing service tests**

Use a small fake Mattermost client. Verify independent server bootstrap, team ordering, team channel sections, a server-wide DM section, and derived DM names from participant users.

**Step 2: Verify RED**

Run: `go test ./internal/service -run 'Test(ServerBootstrap|BuildChannelSections|DirectMessageName)' -v`

Expected: FAIL because the Mattermost services do not exist.

**Step 3: Implement minimal services**

Keep the client interface local to the service package and limited to required methods. Do not create a provider abstraction.

**Step 4: Verify GREEN**

Run the focused service tests and expect PASS.

**Step 5: Commit**

```bash
git add internal/service/server.go internal/service/server_test.go internal/service/channels.go internal/service/channels_test.go
git commit -m "feat: bootstrap Mattermost servers and channel sections"
```

### Task 7: Adapt SQLite Cache Schema

**Files:**
- Modify: `internal/cache/cache.go`
- Modify: `internal/cache/cache_test.go`
- Modify or create: `internal/cache/migrations.go`
- Create: `internal/cache/mattermost_test.go`

**Step 1: Write failing cache tests**

Test storing and reading servers, teams, Mattermost channels, users, and posts with string post IDs and millisecond timestamps. Verify replacement and idempotent upserts.

**Step 2: Verify RED**

Run: `go test ./internal/cache -run 'TestMattermost' -v`

Expected: FAIL because the schema still assumes Slack workspaces/messages.

**Step 3: Implement minimal schema transition**

Since this is a new application identity with a new config/cache directory, prefer a clean Mattermost schema over compatibility migration from `slk` databases. Retain reusable SQLite setup and transaction helpers.

**Step 4: Verify GREEN**

Run: `go test ./internal/cache -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/cache
git commit -m "feat: cache Mattermost servers channels and posts"
```

### Task 8: Connect Rail And Sidebar To Mattermost Data

**Files:**
- Modify: `internal/ui/app.go`
- Modify: `internal/ui/app_test.go`
- Modify: `internal/ui/workspace/`
- Modify: `internal/ui/sidebar/`
- Modify: `cmd/mmk/bootstrap_adapters.go`
- Modify: `cmd/mmk/bootstrap_adapters_test.go`

**Step 1: Write failing UI adapter tests**

Verify one rail item per server, collapsible team sections, server-wide DMs, active-server switching, and aggregate unread badges.

**Step 2: Verify RED**

Run: `go test ./internal/ui ./cmd/mmk -run 'Test(Mattermost|ServerRail|TeamSections)' -v`

Expected: FAIL because UI adapters still consume Slack structures.

**Step 3: Implement minimal adapter conversion**

Change UI-facing records to Mattermost-neutral names where practical. Remove Slack-native sidebar sections and always use the Mattermost team grouping.

**Step 4: Verify GREEN and UI suite**

Run: `go test ./internal/ui/... ./cmd/mmk -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/ui cmd/mmk/bootstrap_adapters.go cmd/mmk/bootstrap_adapters_test.go
git commit -m "feat: render Mattermost servers teams and channels"
```

### Task 9: Implement Message History And Pagination

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`
- Create: `internal/service/messages.go`
- Create: `internal/service/messages_test.go`
- Modify: `internal/ui/messages/`

**Step 1: Write failing tests**

Cover `/channels/{id}/posts`, order reconstruction from Mattermost's `order` array, `before` pagination, cache-first loading, deduplication, and millisecond timestamp rendering.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./internal/service ./internal/ui/messages -run 'Test(MessageHistory|OlderMessages|PostOrder)' -v`

Expected: FAIL.

**Step 3: Implement minimal history path**

Fetch posts, map them to compact messages, preserve server order, cache results, and expose older-page loading to the existing viewport behavior.

**Step 4: Verify GREEN**

Run the focused tests, then `go test ./internal/ui/messages ./internal/service ./internal/mattermost`.

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost internal/service/messages.go internal/service/messages_test.go internal/ui/messages
git commit -m "feat: load and paginate Mattermost posts"
```

### Task 10: Send Messages With Pending State

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`
- Modify: `internal/service/messages.go`
- Modify: `internal/service/messages_test.go`
- Modify: `internal/ui/compose/`
- Modify: `cmd/mmk/main.go`

**Step 1: Write failing tests**

Test post creation payloads, pending local messages, success replacement, failed state, retry, and correlation-ID deduplication.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./internal/service ./cmd/mmk -run 'Test(SendMessage|PendingMessage|RetryMessage)' -v`

Expected: FAIL.

**Step 3: Implement minimal sending path**

Use `POST /api/v4/posts`. Do not implement persistent offline queues. Keep failures visible and retryable in memory.

**Step 4: Verify GREEN**

Run the focused tests and then `go test ./...`.

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost internal/service internal/ui/compose cmd/mmk/main.go
git commit -m "feat: send Mattermost messages"
```

### Task 11: Implement WebSocket Authentication And Events

**Files:**
- Create: `internal/mattermost/websocket.go`
- Create: `internal/mattermost/websocket_test.go`
- Create: `internal/mattermost/events.go`
- Create: `internal/mattermost/events_test.go`

**Step 1: Write failing WebSocket tests**

Run a local WebSocket server and verify connection URL conversion, authentication challenge, `posted` decoding, unknown-event tolerance, ping/pong behavior, and clean cancellation.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost -run 'TestWebSocket' -v`

Expected: FAIL because the WebSocket client does not exist.

**Step 3: Implement minimal realtime client**

Use Gorilla WebSocket and typed application events. Keep protocol envelopes private to the package.

**Step 4: Verify GREEN**

Run: `go test ./internal/mattermost -run 'TestWebSocket' -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost/websocket.go internal/mattermost/websocket_test.go internal/mattermost/events.go internal/mattermost/events_test.go
git commit -m "feat: add Mattermost realtime events"
```

### Task 12: Add Reconnect And Reconciliation

**Files:**
- Create: `internal/service/connection.go`
- Create: `internal/service/connection_test.go`
- Modify: `cmd/mmk/main.go`
- Modify: `cmd/mmk/reconnect_sync.go`
- Modify: `cmd/mmk/reconnect_sync_test.go`

**Step 1: Write failing deterministic tests**

Inject a clock/backoff function. Verify independent server failures, exponential delay bounds, cancellation, state transitions, and REST reconciliation after reconnect.

**Step 2: Verify RED**

Run: `go test ./internal/service ./cmd/mmk -run 'Test(Reconnect|Reconciliation|ConnectionState)' -v`

Expected: FAIL.

**Step 3: Implement minimal connection manager**

Use jittered exponential backoff with a cap. Do not sleep directly in tests. Reconcile channel membership, unread metadata, and recent posts after reconnect.

**Step 4: Verify GREEN**

Run the focused tests and expect PASS.

**Step 5: Commit**

```bash
git add internal/service/connection.go internal/service/connection_test.go cmd/mmk/main.go cmd/mmk/reconnect_sync.go cmd/mmk/reconnect_sync_test.go
git commit -m "feat: reconnect and reconcile Mattermost servers"
```

### Task 13: Synchronize Unread And Read State

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`
- Modify: `internal/service/channels.go`
- Modify: `internal/service/channels_test.go`
- Modify: `internal/ui/sidebar/`
- Modify: `cmd/mmk/main.go`

**Step 1: Write failing tests**

Cover unread counts from channel members, new-post increments, channel-view events, opening a channel, and the channel-view REST request.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./internal/service ./internal/ui/sidebar ./cmd/mmk -run 'Test(Unread|MarkChannelRead|ChannelViewed)' -v`

Expected: FAIL.

**Step 3: Implement minimal read-state path**

Treat Mattermost membership metadata as authoritative and update optimistic UI when a channel is opened. Correct it from realtime/REST responses.

**Step 4: Verify GREEN**

Run the focused tests and then `go test ./...`.

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost internal/service internal/ui/sidebar cmd/mmk/main.go
git commit -m "feat: synchronize Mattermost unread state"
```

### Task 14: Add Thread Viewing And Replies

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`
- Modify: `internal/service/messages.go`
- Modify: `internal/service/messages_test.go`
- Modify: `internal/ui/thread/`
- Modify: `cmd/mmk/main.go`

**Step 1: Write failing tests**

Test thread fetch order, root message handling, replies using `root_id`, realtime reply routing, and thread-panel state.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./internal/service ./internal/ui/thread ./cmd/mmk -run 'Test(Thread|Reply)' -v`

Expected: FAIL.

**Step 3: Implement minimal thread path**

Use Mattermost thread endpoints and post creation with `root_id`. Keep the workspace-wide Slack Threads view disabled until a Mattermost equivalent is deliberately designed.

**Step 4: Verify GREEN**

Run the focused tests and then `go test ./...`.

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/mattermost internal/service internal/ui/thread cmd/mmk/main.go
git commit -m "feat: support Mattermost threads"
```

### Task 15: Remove Slack Runtime And Dependencies

**Files:**
- Delete: `internal/slack/`
- Delete: `internal/slackdesktop/`
- Delete: `internal/slackfmt/`
- Delete: `internal/slackhttp/`
- Delete: `internal/slackurl/`
- Delete or replace: `internal/ui/messages/blockkit/`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: all remaining files reported by Slack-name searches

**Step 1: Add a failing repository guard test**

Create a test or verification script that fails when production Go files import `github.com/slack-go/slack` or internal Slack packages. Permit historical docs and attribution where appropriate.

**Step 2: Verify RED**

Run the guard and confirm it reports existing Slack runtime dependencies.

**Step 3: Remove Slack-only code**

Delete unused packages, remove `slack-go`, replace Slack mrkdwn rendering with Mattermost Markdown behavior, and remove unsupported Slack Block Kit controls.

**Step 4: Verify GREEN**

Run the guard, `go mod tidy`, and `go test ./...`.

Expected: no Slack runtime imports and all tests PASS.

**Step 5: Commit**

```bash
git add -A
git commit -m "refactor: remove Slack runtime support"
```

### Task 16: Documentation And End-To-End Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/STATUS.md`
- Create: `docs/configuration.md`
- Create: `docs/development.md`
- Modify: `.goreleaser.yaml`
- Modify: `LICENSE` only if adding a separate attribution notice without changing upstream terms

**Step 1: Write or update documentation checks**

Ensure install commands build `cmd/mmk`, examples use `mmk --add-server`, and no user-facing setup instructs users to install Slack Desktop or provide Slack tokens.

**Step 2: Run static searches**

Search production docs and configuration examples for stale `slk`, Slack authentication, and Slack API instructions. Attribution references are expected and should remain.

**Step 3: Complete documentation**

Document PAT creation, URL format, credential storage, multiple servers/teams, supported MVP features, known limitations, and derivation from `gammons/slk`.

**Step 4: Run full verification**

Run:

```bash
go test ./...
go vet ./...
go build ./cmd/mmk
```

Expected: all commands exit successfully with no warnings.

If Mattermost smoke-test credentials are available, run the opt-in smoke test without printing or persisting secrets.

**Step 5: Commit**

```bash
git add README.md docs .goreleaser.yaml LICENSE
git commit -m "docs: document mmk setup and development"
```
