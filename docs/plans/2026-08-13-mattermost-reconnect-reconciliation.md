# Mattermost Reconnect And Reconciliation Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Give every Mattermost server an independent authenticated WebSocket reconnect loop with deterministic capped jittered backoff, non-destructive REST reconciliation, and cache-safe realtime delivery.

**Architecture:** Add a transport-agnostic connection manager under `internal/service`, expose an explicit authenticated-ready callback from the Mattermost WebSocket client after the matching server acknowledgement, and wire one manager per bootstrapped server from `cmd/mmk`. Reconciliation replaces authoritative metadata without resetting retained active-channel UI state and fetches recent posts only for the active channel of the active server. Realtime events persist through a trusted-server atomic cache operation that creates hidden inactive channel placeholders when bootstrap metadata is late. Failed recent REST reads return cached messages with the error for non-authoritative UI merging.

**Tech Stack:** Go, Gorilla WebSocket, Bubble Tea messages, SQLite cache, table-driven tests, `httptest`.

---

### Task 1: Expose Authenticated WebSocket Readiness

**Files:**
- Modify: `internal/mattermost/websocket.go:32-111`
- Modify: `internal/mattermost/websocket_test.go`
- Modify: call sites found by `grep -R "RunWebSocket" --include='*.go'`

**Step 1: Write the failing readiness tests**

Add tests that call the new API with separate `onReady` and event callbacks:

```go
func TestWebSocketSignalsReadyAfterAuthentication(t *testing.T) {
    // Upgrade, read authentication_challenge, then assert onReady fires once.
}

func TestWebSocketDoesNotSignalReadyWhenAuthenticationWriteFails(t *testing.T) {
    // Close after upgrade before the client can complete its auth write.
}
```

Update existing tests to pass `func() {}` as the readiness callback. Also test
that `nil` readiness and event handlers return validation errors rather than
panic.

**Step 2: Run tests to verify RED**

Run:

```bash
go test ./internal/mattermost -run 'TestWebSocket' -count=1 -v
```

Expected: FAIL to compile because `RunWebSocket` does not accept a readiness
callback.

**Step 3: Implement the minimal API**

Change the signature to:

```go
func (c *Client) RunWebSocket(ctx context.Context, onReady func(), handle func(Event)) error
```

Validate both callbacks. After `WriteJSON` of the authentication challenge,
wait for JSON `{"status":"OK","seq_reply":1}` before invoking `onReady()`
exactly once and entering the application-event read loop. Discard pre-ack
events, sanitize rejected/malformed acknowledgement errors, and do not invoke
readiness from dial, write, cancellation, or rejection paths. Every successful
WebSocket test server must send the acknowledgement before events, pings, or
waiting for cancellation.

**Step 4: Run tests to verify GREEN**

Run:

```bash
go test ./internal/mattermost -run 'TestWebSocket' -count=1 -v
```

Expected: PASS.

**Step 5: Commit checkpoint**

```bash
git add internal/mattermost/websocket.go internal/mattermost/websocket_test.go
git commit -m "refactor: expose Mattermost WebSocket readiness"
```

### Task 2: Implement The Deterministic Connection Manager

**Files:**
- Create: `internal/service/connection.go`
- Create: `internal/service/connection_test.go`

**Step 1: Write failing lifecycle tests**

Define a scripted fake implementing:

```go
type ConnectionClient interface {
    RunWebSocket(context.Context, func(), func(mattermost.Event)) error
}
```

Tests must cover:

```go
func TestConnectionStateFirstConnect(t *testing.T)
func TestReconnectRunsReconciliationAfterReady(t *testing.T)
func TestReconnectBackoffIsExponentialAndCapped(t *testing.T)
func TestReconnectCancellationInterruptsWait(t *testing.T)
func TestReconnectResetsBackoffAfterReady(t *testing.T)
func TestReconciliationFailureDoesNotStopConnectedSocket(t *testing.T)
func TestConnectionManagersFailIndependently(t *testing.T)
```

Use channels and a fake waiter; do not use `time.Sleep`.

**Step 2: Run tests to verify RED**

Run:

```bash
go test ./internal/service -run 'Test(ConnectionState|Reconnect|ConnectionManagers)' -count=1 -v
```

Expected: FAIL because the connection manager does not exist.

**Step 3: Implement the manager**

Create a dependency struct with explicit seams:

```go
type ConnectionManager struct {
    Client    ConnectionClient
    OnEvent   func(mattermost.Event)
    OnState   func(mattermost.ConnectionState)
    Reconcile func(context.Context) error // prompt enqueue only
    OnError   func(error)
    Wait      func(context.Context, time.Duration) error
    Jitter    func(time.Duration) time.Duration
}
```

Provide `Run(ctx)` and default helpers for production waiting and bounded
jitter. Keep constants at `1*time.Second` and `30*time.Second`. The manager
 must publish `Connecting` once, reset attempts from the authenticated-ready
callback, enqueue reconciliation only on ready callbacks after the first ready
connection, publish `Connected` promptly, and exit with `ctx.Err()` on
cancellation.

Keep state callbacks synchronous so each server observes deterministic order.
Do not add goroutines inside the manager; the caller owns its goroutine.

**Step 4: Run tests to verify GREEN**

Run:

```bash
go test ./internal/service -run 'Test(ConnectionState|Reconnect|ConnectionManagers)' -count=1 -v
```

Expected: PASS.

**Step 5: Run race coverage for the manager**

Run:

```bash
go test -race ./internal/service -run 'Test(ConnectionState|Reconnect|ConnectionManagers)' -count=1
```

Expected: PASS with no race reports.

**Step 6: Commit checkpoint**

```bash
git add internal/service/connection.go internal/service/connection_test.go
git commit -m "feat: add Mattermost connection manager"
```

### Task 3: Add A Mattermost Reconciliation Adapter

**Files:**
- Modify: `cmd/mmk/mattermost_startup.go:141-295`
- Modify: `cmd/mmk/mattermost_history.go:16-65`
- Modify: `cmd/mmk/mattermost_startup_test.go`
- Create or modify: `cmd/mmk/reconnect_sync_test.go`

**Step 1: Write failing reconciliation tests**

Add Mattermost-specific tests without deleting unrelated Slack regression
coverage. Verify:

```go
func TestReconciliationReplacesAuthoritativeSnapshot(t *testing.T)
func TestReconciliationFetchesOnlyActiveServerChannel(t *testing.T)
func TestReconciliationSkipsHistoryForInactiveServer(t *testing.T)
func TestReconciliationPersistsBeforeServerRefresh(t *testing.T)
func TestReconciliationFailureKeepsPreviousUsableSnapshot(t *testing.T)
```

Use a fake startup client that records bootstrap and `ChannelPosts` calls. Seed
the cache with a membership/channel omitted by the server response and assert
that replacement retires it while cached post history remains.

**Step 2: Run tests to verify RED**

Run:

```bash
go test ./cmd/mmk -run 'TestReconciliation' -count=1 -v
```

Expected: FAIL because the Mattermost reconciliation adapter does not exist.

**Step 3: Implement a focused reconciliation method**

Add a method on `mattermostStartup` or a small adjacent struct that receives:

```go
type mattermostReconcileDeps struct {
	Cache           mattermostReconcileStore
	Send            func(tea.Msg)
	Clock           func() time.Time
	ActiveSelection func() (ids.ServerID, string)
}
```

`ActiveSelection` must return the server/channel pair from one concurrency-safe
snapshot. Separate accessors are not permitted because a workspace switch can
otherwise produce a torn pair.

The method must:

1. Read the existing server/client under `RLock`, then release the lock before
   network calls.
2. Run `service.BootstrapServer`.
3. Persist with `ReplaceMattermostBootstrapSnapshotContext`.
4. Replace the in-memory snapshot only after persistence succeeds.
5. Send `ui.ServerRefreshedMsg` after cache and memory are updated.
6. Read the active server/channel/generation snapshot once and fetch one recent
   page through `service.NewMattermostHistoryService` only when the server
   matches and the channel ID is nonempty.
7. Send the exact page in `ui.MattermostReconciledHistoryMsg`. The UI reducer
   scopes it against the current active server/channel/selection generation and
   delegates to the existing recent-page merge path. On REST failure, cached
   fallback messages merge non-authoritatively with no deletion or exhaustion
   semantics.

Do not hold `mattermostStartup.mu` across REST or SQLite operations.

Normal reconnect requests pass through one pre-registered server-scoped worker.
One request may run while one pending follow-up is retained. The readiness
callback only enqueues, so WebSocket heartbeat and event reads continue while
REST/cache/UI reconciliation is blocked. Concurrent direct requests still
receive a per-server generation under the server's apply
mutex when `reconcile` is invoked. The mutex is released before bootstrap, so
REST calls overlap after their generations are issued. Completion reacquires
the same mutex, checks its generation, and holds the mutex through
cache/memory/UI/history application. This makes generation issuance atomic
with the stale check: an invocation cannot issue a newer generation after an
older completion passes its check but before persistence. Discard a completed
request with `errMattermostReconciliationSuperseded` when a newer generation
was issued first. Different servers use independent apply locks. Validate all
adapter dependencies, including typed-nil cache values, before starting work.

**Step 4: Run tests to verify GREEN**

Run:

```bash
go test ./cmd/mmk -run 'TestReconciliation' -count=1 -v
```

Expected: PASS.

**Step 5: Commit checkpoint**

```bash
git add cmd/mmk/mattermost_startup.go cmd/mmk/mattermost_history.go cmd/mmk/mattermost_startup_test.go cmd/mmk/reconnect_sync_test.go
git commit -m "feat: reconcile Mattermost state after reconnect"
```

### Task 4: Wire Per-Server Realtime Managers Into Startup

**Files:**
- Modify: `cmd/mmk/mattermost_startup.go:23-91,151-295`
- Modify: `cmd/mmk/main.go` only if active server/channel accessors are not
  already reachable through `ui.App`
- Modify: `cmd/mmk/mattermost_startup_test.go`
- Modify: `internal/ui/msgs.go` only if a server-scoped realtime-state message
  is required
- Modify: `internal/ui/reducer_workspace.go` only if that new message is added

**Step 1: Write failing wiring and shutdown tests**

Add tests that prove:

```go
func TestStartupRunsIndependentMattermostConnections(t *testing.T)
func TestStartupForwardsEventsWithServerIdentity(t *testing.T)
func TestStartupWaitIncludesConnectionManagers(t *testing.T)
func TestRuntimeDisconnectPreservesUsableCachedServer(t *testing.T)
```

The first test scripts one server to fail repeatedly while another reaches
ready and receives events. The shutdown test cancels startup while one manager
is in fake backoff and verifies `WaitContext` returns.

**Step 2: Run tests to verify RED**

Run:

```bash
go test ./cmd/mmk -run 'Test(StartupRunsIndependentMattermostConnections|StartupForwardsEvents|StartupWaitIncludesConnectionManagers|RuntimeDisconnect)' -count=1 -v
```

Expected: FAIL because startup does not run WebSockets.

**Step 3: Wire managers after successful bootstrap**

Extend `mattermostStartupClient` with the new `RunWebSocket` signature. Start
one manager per successfully initialized client under the shared startup
context and account for it in `startup.wg`.

Callbacks must capture the configured server ID explicitly. Runtime failures
must not set the rail to bootstrap error or clear `usable`; cached state remains
switchable. Route diagnostics through existing debug logging without including
tokens.

Expose active server/channel access through narrow functions. Prefer methods on
`ui.App` that already exist; add the smallest safe accessor only if necessary.

**Step 4: Run tests to verify GREEN**

Run:

```bash
go test ./cmd/mmk -run 'Test(StartupRunsIndependentMattermostConnections|StartupForwardsEvents|StartupWaitIncludesConnectionManagers|RuntimeDisconnect)' -count=1 -v
```

Expected: PASS.

**Step 5: Commit checkpoint**

```bash
git add cmd/mmk/mattermost_startup.go cmd/mmk/mattermost_startup_test.go cmd/mmk/main.go internal/ui/msgs.go internal/ui/reducer_workspace.go
git commit -m "feat: run Mattermost realtime connections"
```

### Task 5: Persist And Render Realtime Posted Events

**Files:**
- Create or modify: `cmd/mmk/mattermost_events.go`
- Create or modify: `cmd/mmk/mattermost_events_test.go`
- Modify: `cmd/mmk/mattermost_startup.go`
- Modify: `cmd/mmk/mattermost_adapters.go` if a message adapter is shared

**Step 1: Write failing event tests**

Cover:

```go
func TestMattermostPostedEventPersistsForInactiveServer(t *testing.T)
func TestMattermostPostedEventRefreshesActiveChannel(t *testing.T)
func TestMattermostPostedEventDeduplicatesWithReconciliation(t *testing.T)
func TestMattermostEventCannotMutateAnotherServer(t *testing.T)
```

Use opaque post IDs and assert one cached row after both a realtime event and a
recent-history reconciliation return the same post.

**Step 2: Run tests to verify RED**

Run:

```bash
go test ./cmd/mmk -run 'TestMattermost(PostedEvent|Event)' -count=1 -v
```

Expected: FAIL because startup currently has no Mattermost event adapter.

**Step 3: Implement the minimal event adapter**

Convert `mattermost.Message` to the existing Mattermost cache representation
and upsert it with server identity taken from the manager callback, not trusted
from event payload fields. Persist before notifying UI. For the active channel,
reuse the existing history refresh message/path rather than introducing a
second message-list merge algorithm.

Use `UpsertMattermostRealtimePostContext` for realtime persistence. In one
context-aware transaction, verify the trusted server row exists, insert an
inactive direct-kind placeholder channel when necessary, and upsert the post.
The placeholder must remain absent from active bootstrap/sidebar queries. A
later `ReplaceMattermostBootstrapSnapshot` must enrich/reactivate the row and
retain its posts. Keep all existing foreign keys enabled.

When recent REST refresh fails, read the cached recent page and return its
messages together with the live error. The UI reducer applies an error result's
messages only as a non-authoritative deduplicating merge, reports the failure,
and must not run authoritative ID/deletion reconciliation or mark history
exhausted. Selection, viewport, anchors, transient rows, and refresh coalescing
remain intact. Empty cache plus REST failure leaves the existing pane unchanged.

The connection manager callback must enqueue only. Start one server-scoped,
tracked worker before its manager can deliver events. Use an unbounded
mutex-protected FIFO plus a buffered wake signal so enqueue is O(1), does not
block on SQLite or Bubble Tea, and cannot lose events because a bounded queue is
full. Register every worker and startup/manager goroutine in the wait group
before launching any of them. This includes event, reconciliation, and
bootstrap/connection workers. The event worker owns persistence and UI notification,
continues after a persistence failure, and discards queued remainder on startup
context cancellation.

Add a context-aware cache write (`UpsertMattermostPostContext`) and use it from
the worker so SQLite lock waits cancel during shutdown. Keep the existing
method as a background-context compatibility wrapper. Diagnostics must not
include post text, opaque IDs, correlations, or credentials.

Malformed supported application frames emit a fixed payload-free diagnostic.
Retain decoder errors for unit tests, continue after isolated malformed frames,
reset the consecutive counter after any valid decoded frame, and terminate the
socket attempt after eight consecutive malformed frames. Transport and
read-limit errors remain terminal.

Add `ReplaceMattermostBootstrapSnapshotContext` and
`UpsertMattermostHistoryContext`; route reconciliation transaction statements
through the supplied context. Keep background wrappers for existing callers.
Cache cancellation tests use a package-private nil-in-production pre-write hook
to prove the operation reached its lock attempt before cancellation.

**Step 4: Run tests to verify GREEN**

Run:

```bash
go test ./cmd/mmk -run 'TestMattermost(PostedEvent|Event)' -count=1 -v
```

Expected: PASS.

Additional focused coverage:

```go
func TestMattermostEventDispatcherEnqueueReturnsWhilePersistenceBlocked(t *testing.T)
func TestMattermostEventDispatcherPreservesFIFO(t *testing.T)
func TestMattermostEventDispatcherContinuesAfterPersistenceFailure(t *testing.T)
func TestMattermostEventDispatcherCancellationDiscardsQueuedEvents(t *testing.T)
func TestStartupEventCallbackReturnsPromptlyAndWaitTracksWorker(t *testing.T)
func TestWebSocketContinuesAfterMalformedPostedFrame(t *testing.T)
func TestMattermostHistoryRefreshBurstCoalescesWithoutLosingFinalRefresh(t *testing.T)
func TestMattermostMigrationUpgradesV4PostsWithEmptyCorrelation(t *testing.T)
func TestStartupReconnectReadsEventsWhileReconciliationBlocked(t *testing.T)
func TestMattermostReconcileDispatcherCoalescesPendingRequests(t *testing.T)
func TestMattermostSnapshotContextReplacementCancelsWhileDatabaseLocked(t *testing.T)
func TestMattermostHistoryContextWriteCancelsWhileDatabaseLocked(t *testing.T)
func TestWebSocketStopsAfterConsecutiveMalformedFrameBudget(t *testing.T)
func TestWebSocketValidUnknownEventResetsMalformedFrameBudget(t *testing.T)
```

**Step 5: Commit checkpoint**

```bash
git add cmd/mmk/mattermost_events.go cmd/mmk/mattermost_events_test.go cmd/mmk/mattermost_startup.go cmd/mmk/mattermost_adapters.go
git commit -m "feat: apply Mattermost realtime posts"
```

### Task 6: Verify Task 12 End To End

**Files:**
- Modify: `docs/STATUS.md`
- Review: all files changed since `897a2f0`

**Step 1: Run focused Task 12 tests**

Run:

```bash
go test ./internal/mattermost ./internal/service ./cmd/mmk -run 'Test(WebSocket|Reconnect|Reconciliation|ConnectionState|ConnectionManagers|StartupRunsIndependentMattermostConnections|StartupForwardsEvents|StartupWaitIncludesConnectionManagers|RuntimeDisconnect|MattermostPostedEvent|MattermostEvent)' -count=1 -v
```

Expected: PASS.

**Step 2: Run race tests**

Run:

```bash
go test -race ./internal/mattermost ./internal/service ./cmd/mmk -run 'Test(WebSocket|Reconnect|Reconciliation|ConnectionState|ConnectionManagers|StartupRunsIndependentMattermostConnections|StartupForwardsEvents|StartupWaitIncludesConnectionManagers|RuntimeDisconnect|MattermostPostedEvent|MattermostEvent)' -count=1
```

Expected: PASS with no race reports.

**Step 3: Run repository verification**

Run each command separately and require exit code zero:

```bash
go test ./... -count=1
go vet ./...
go build ./...
```

Expected: all PASS.

**Step 4: Update migration status**

Mark Task 12 complete in `docs/STATUS.md` and summarize the actual implemented
scope: per-server connection managers, capped jittered reconnect, authoritative
metadata reconciliation, active-channel recent-post catch-up, and realtime
post persistence.

**Step 5: Review secret safety and worktree diff**

Run:

```bash
git status --short --branch
git diff --check
git diff --stat 897a2f0
```

Inspect the diff for accidental PATs, temporary smoke tools, generated files,
non-ASCII reconnect markers, or unrelated changes. Do not print any credential
value while checking.

**Step 6: Final commit**

If the checkpoint commits were intentionally skipped, create the Task 12
commit requested by the parent migration plan:

```bash
git add internal/mattermost internal/service cmd/mmk internal/ui docs/STATUS.md docs/plans/2026-08-13-mattermost-reconnect-reconciliation-design.md docs/plans/2026-08-13-mattermost-reconnect-reconciliation.md
git commit -m "feat: reconnect and reconcile Mattermost servers"
```

Do not commit or push unless explicitly requested in the active session.
