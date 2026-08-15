# Mattermost Unread And Read State Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Synchronize Mattermost unread state from memberships, realtime posts, channel-view events, and local channel opens while preserving REST/reconciliation as authoritative correction.

**Architecture:** Keep Mattermost channel membership metadata authoritative and maintain incremental state in the retained per-server runtime snapshot. Derive the existing boolean sidebar state through pure service helpers, clear it optimistically on channel selection, and apply REST/realtime corrections through one server-scoped update path. Do not reuse Slack timestamp read APIs or enable Mattermost mark-unread.

**Tech Stack:** Go, Bubble Tea, Mattermost REST API v4, Mattermost WebSocket events, SQLite cache, standard `testing` package.

---

### Task 1: Define And Test Read-State Transitions

**Files:**
- Modify: `internal/service/channels.go`
- Modify: `internal/service/channels_test.go`

1. Add failing table tests named `TestUnread...` for membership-derived unread state: mentions, message-count divergence, equal counts, and absent membership.
2. Add failing tests for pure transitions: inactive-channel post becomes unread, active-channel post stays read, and viewed state clears unread/mentions without mutating unrelated channels.
3. Run `go test ./internal/service -run 'TestUnread' -count=1 -v` and confirm failures are caused by missing helpers.
4. Add the smallest pure helpers needed by runtime/bootstrap code and replace the duplicate startup predicate with the shared derivation.
5. Re-run the focused service tests and confirm PASS.

### Task 2: Add The Official Channel-View REST Operation

**Files:**
- Modify: `internal/mattermost/client.go`
- Modify: `internal/mattermost/client_test.go`

1. Add failing `TestMarkChannelRead...` tests for `POST /api/v4/channels/members/{user_id}/view`.
2. Assert JSON fields `channel_id`, `prev_channel_id`, and `collapsed_threads_supported`; assert decoding of `status` and `last_viewed_at_times`.
3. Add validation/error tests that prove no request is made for invalid IDs and malformed authoritative responses are rejected.
4. Run `go test ./internal/mattermost -run 'TestMarkChannelRead' -count=1 -v` and confirm RED.
5. Implement a small `ViewChannel` request/result API using the existing authenticated JSON request path. Set `collapsed_threads_supported` to false until thread semantics are deliberately implemented.
6. Re-run the focused client tests and confirm PASS.

### Task 3: Decode Authoritative Viewed Events

**Files:**
- Modify: `internal/mattermost/events.go`
- Modify: `internal/mattermost/events_test.go`
- Modify: `internal/mattermost/websocket_test.go`

1. Add failing `TestChannelViewed...` tests for current `multiple_channels_viewed` frames with `data.channel_times` and `broadcast.user_id`.
2. Add a narrow legacy `channel_viewed` fixture that normalizes a channel ID without inventing an authoritative timestamp.
3. Add malformed payload tests while preserving unknown-event tolerance and payload redaction.
4. Run `go test ./internal/mattermost -run 'TestChannelViewed' -count=1 -v` and confirm RED.
5. Add one normalized typed event carrying user ID and channel view updates, then decode both supported wire names.
6. Re-run decoder and WebSocket tests and confirm PASS.

### Task 4: Apply Incremental Runtime Read State

**Files:**
- Modify: `cmd/mmk/mattermost_startup.go`
- Modify: `cmd/mmk/mattermost_events.go`
- Modify: `cmd/mmk/mattermost_events_test.go`
- Modify: `cmd/mmk/mattermost_startup_test.go`
- Modify: `cmd/mmk/mattermost_reconcile_test.go`

1. Add failing tests named `TestUnread...` and `TestChannelViewed...` for inactive-channel posts, inactive-server posts, active-channel suppression, viewed-event correction, retained snapshot updates, and reconnect authoritative correction.
2. Require duplicate `posted` delivery not to double-increment effective unread state. Use existing post persistence/deduplication evidence rather than a blind counter increment.
3. Run focused `cmd/mmk` tests and confirm RED.
4. Add a locked, server-scoped runtime snapshot update path that applies service transitions and returns a fresh `ui.ServerViewState`.
5. Extend the event adapter to apply posted/viewed transitions before UI notification. Keep active history refresh behavior unchanged.
6. Do not write optimistic clears through the generic equal-revision membership upsert; authoritative bootstrap/reconciliation remains the durable correction path.
7. Re-run focused tests and confirm PASS.

### Task 5: Optimistically Mark Opened Channels Read

**Files:**
- Modify: `internal/ui/services.go`
- Modify: `internal/ui/app.go`
- Modify: `internal/ui/reducer_channels.go`
- Modify: `internal/ui/reducer_workspace.go`
- Modify: `internal/ui/mattermost_history_test.go`
- Modify: `internal/ui/sidebar/model_test.go`
- Modify: `cmd/mmk/mattermost_startup.go`
- Add or modify: `cmd/mmk/mattermost_read_test.go`

1. Add failing `TestMarkChannelRead...` tests proving channel selection clears the sidebar immediately, preserves other unread channels, sends same-server previous channel only, and issues no Slack read or mark-unread operation.
2. Add a focused sidebar invalidation test only if the selected state-owner API requires it; do not add provider-specific rendering.
3. Run the plan's focused command and confirm RED for missing optimistic/wiring behavior.
4. Add a narrow Mattermost read service callback to the app and invoke it only from the Mattermost selection branch.
5. Optimistically update the retained snapshot/UI synchronously, then serialize the REST `ViewChannel` request so rapid navigation cannot reorder server active-channel state.
6. Apply returned view times as correction. On REST failure, retain optimistic UX and rely on viewed events/reconciliation for authoritative correction; report the sanitized error diagnostically.
7. Update inactive `ServerRefreshedMsg` handling so the target workspace rail unread state is refreshed before the active-server guard.
8. Re-run focused tests and confirm PASS.

### Task 6: Reviews And Verification

**Files:**
- Review all Task 13 changes

1. Dispatch an independent spec reviewer against Task 13 requirements and this plan; fix every gap and re-review.
2. Dispatch an independent code-quality reviewer; fix every important issue and re-review.
3. Run:

```bash
go test ./internal/mattermost ./internal/service ./internal/ui/sidebar ./cmd/mmk \
  -run 'Test(Unread|MarkChannelRead|ChannelViewed)' -count=1 -v
go test -race ./internal/mattermost ./internal/service ./internal/ui/sidebar ./cmd/mmk
go test ./...
go vet ./...
go build ./...
```

4. Confirm `git status --short` contains only intended Task 13 changes.
5. Commit and push only after all reviews and verification pass and only when explicitly requested.
