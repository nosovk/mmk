# Mattermost Reconnect And Reconciliation Design

## Scope

Task 12 adds an independent realtime lifecycle for every configured Mattermost
server. A server reconnects with capped jittered exponential backoff and uses
REST reconciliation after a recovered WebSocket connection because Mattermost
does not replay every event missed while the socket was offline.

The first implementation reconciles complete server metadata and recent posts
for only the active channel of the active server. It does not sweep post
history for every membership channel.

## Architecture

`internal/service/connection.go` owns the generic connection loop. It depends
on a narrow WebSocket runner and callbacks for events, state transitions, and
reconciliation. It does not know about Bubble Tea, SQLite, credentials, or the
Mattermost REST model beyond typed events and connection states.

Each configured server gets its own connection manager and goroutine. A broken
server therefore cannot delay another server's connection lifecycle or block
cached UI access.

`mattermostStartup` remains the runtime owner of server clients and snapshots.
After the initial REST bootstrap succeeds, it starts the server's connection
manager with the existing authenticated client. The same client retains proxy,
TLS, cookie-jar, and connection settings for REST and WebSocket traffic.

The lifecycle is:

```text
REST bootstrap
  -> Connecting
  -> WebSocket authenticated
  -> Connected
  -> socket failure
  -> Offline
  -> Reconnecting
  -> jittered capped delay
  -> WebSocket authenticated
  -> Connected
  -> asynchronous REST reconciliation
```

The initial WebSocket connection does not repeat the completed REST bootstrap.
Reconciliation runs only after a connection has previously reached the ready
state and then reconnects. Recovered readiness enqueues work and publishes
`Connected` promptly; it does not run REST, SQLite, or UI work on the WebSocket
read goroutine.

## Connection Readiness

`Client.RunWebSocket` returns only after failure or cancellation, so the
connection manager uses a readiness callback to identify an authenticated
connection. The client writes challenge sequence 1, waits for a matching JSON
acknowledgement with `status: "OK"` and `seq_reply: 1`, then invokes readiness
exactly once before delivering application events. Pre-ack application frames
are discarded. Rejected or malformed acknowledgements return sanitized errors
that do not expose response payloads or credentials and therefore enter the
normal connection-manager backoff path.

The callback distinguishes a failed dial/authentication attempt from a usable
connection without timers or protocol guesses. Existing callers and tests are
updated directly; no compatibility wrapper is required because this is an
internal API with no external consumers.

## Backoff And Cancellation

Reconnect delay starts at one second, doubles after consecutive failed
attempts, and is capped at 30 seconds:

```text
1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
```

A bounded symmetric jitter is applied without allowing a negative delay or a
delay above the cap. The jitter source and waiter are injected so tests never
sleep. A successful authenticated connection resets the failure attempt count.
Cancellation interrupts both an active WebSocket and a pending backoff wait.
Cancellation exits silently without publishing another state.

## State Transitions

State changes are server-scoped and emitted in order:

```text
first connection: Connecting -> Connected
after disconnect: Offline -> Reconnecting -> Connected
failed retry:     Offline -> Reconnecting -> Offline -> Reconnecting -> ...
```

The rail continues to use its existing ready/error lifecycle for bootstrap
availability. Realtime connection state is carried separately so a temporarily
offline server remains browsable from SQLite instead of becoming an unusable
bootstrap error.

## Reconciliation

Each server owns one pre-registered reconciliation dispatcher/worker. The
connection manager readiness callback only enqueues. One reconciliation may run
while at most one pending follow-up is retained, preventing both heartbeat
blocking and unbounded duplicate work. The worker performs these steps:

1. Run `service.BootstrapServer` to obtain authoritative teams, channels,
   current-user memberships, unread metadata, direct-message users, and group
   participants.
2. Persist the result with `ReplaceMattermostBootstrapSnapshotContext`, retiring
   memberships and active entities absent from the complete response while
   preserving cached post history.
3. Replace the in-memory server snapshot and send `ui.ServerRefreshedMsg` so
   the rail, sidebar, channel finder, names, and unread indicators update.
4. If this is the active server and it has an active channel, capture the
   atomic server/channel selection generation and fetch one recent page through
   the existing Mattermost history service. This path writes posts to cache and
   resolves authors.
5. Send that exact page to the Bubble Tea loop in a dedicated reconciliation
   message. The reducer applies it through the normal recent-page merge path
   only when server, channel, and selection generation still match. A failed
   REST read carries cached fallback messages but is merged non-authoritatively,
   without deletions or exhaustion.
6. Do not fetch posts for inactive servers or non-active channels. Their cache
   remains immediately readable and the normal channel-open path revalidates
   them on demand.

The active server, channel, and selection generation are read through one
concurrency-safe selection callback. This produces one atomic snapshot and
avoids combining values observed on opposite sides of a selection change.

Reconciliation requests are generation-scoped per server. The generation is
assigned under the server-specific apply mutex when the adapter is invoked.
The mutex is then released before REST. The dispatcher serializes normal
reconnect requests, while the generation check remains defensive for direct or
future independent callers. Completion reacquires the apply mutex and holds it
through cache, memory, UI, and history application. Apply locks are independent
between servers, and `mattermostStartup.mu` is never held over REST or SQLite.

The metadata refresh may make several bounded bootstrap calls, including one
membership request per team. Post-history catch-up remains O(1) with respect to
the number of channels.

## Realtime Events

Each configured server owns one tracked event worker and one unbounded FIFO
queue implemented as a mutex-protected slice plus a buffered wake channel. The
WebSocket callback only appends the value event and signals the worker, so it
does not run SQLite or Bubble Tea `Send`, does not block on a bounded channel,
and does not silently drop posted events. Workers are registered in the startup
wait group before any startup goroutine begins, eliminating the `Add` versus
`Wait` race. On cancellation, a worker interrupts an in-flight context-aware
SQLite write and may discard queued events so shutdown remains bounded.

The worker processes events serially in server order. `PostedEvent` updates the
cache independently of which server or channel is visible, using the trusted
server ID captured by the connection manager rather than the payload server
field. Persistence errors emit a payload-free diagnostic and do not prevent a
later queued event from being processed. UI notification follows successful
persistence and is gated by the atomically loaded active server/channel pair.
The notification asks the Bubble Tea reducer to run the existing recent-history
fetch and reconciliation path. Refresh requests received while a fetch is in
flight coalesce into one follow-up; a request received during that follow-up
schedules one final fetch rather than being lost.

If a realtime post references a channel absent from the current bootstrap, the
event worker uses `UpsertMattermostRealtimePostContext`. The operation first
validates that the trusted connection server exists, then atomically inserts an
inactive direct-kind placeholder channel and the post. It never trusts a server
ID from the event payload and does not weaken foreign keys. Active bootstrap
queries exclude the placeholder, so it cannot appear in the sidebar. A later
authoritative replacement enriches and reactivates the same channel row while
the post remains cached.

REST reconciliation and WebSocket delivery deduplicate naturally by opaque
Mattermost post ID. If the same post arrives from both paths, the existing
upsert and authoritative-history logic keeps one cached message.

Application-frame decode errors remain useful from `decodeWebSocketEvent` for
unit tests and callers. `RunWebSocket` emits a fixed payload-free diagnostic
and continues after an isolated malformed frame. Any valid decoded frame,
including an unknown event, resets the consecutive counter. Eight consecutive
malformed frames terminate the socket attempt so reconnect can repair a corrupt
stream. Transport reads, cancellation, and oversized frames remain terminal.

## Error Handling

Dial, authentication, and socket-read failures drive reconnect. Their text is
reported without including PAT values.

Reconciliation is best effort once the WebSocket has recovered. A REST or
cache failure is reported through a diagnostic callback, but it does not close
the healthy socket or prevent transition to `Connected`. A later reconnect or
normal channel open gets another opportunity to repair stale data.

An active-channel recent-history REST failure falls back to the cached recent
page. The service returns cached messages together with the live error. The UI
applies those messages as a non-authoritative merge, reports that cached data is
being shown, and deliberately skips authoritative ID and deletion
reconciliation. This preserves selection, viewport, older pages, transient
delivery rows, and refresh coalescing while still making a just-persisted
realtime post visible.

Adapter dependencies are validated before use, including typed-nil cache
implementations. History reconciliation runs after metadata persistence,
in-memory replacement, and UI refresh. A history failure is returned for
diagnostics while retaining the newly committed metadata snapshot.

Realtime cache writes use `UpsertMattermostPostContext`, snapshot replacement
uses `ReplaceMattermostBootstrapSnapshotContext`, and recent-history persistence
uses `UpsertMattermostHistoryContext`. Their transactions and statements use
the worker context, allowing shutdown to cancel SQLite lock waits. Existing
synchronous methods remain `context.Background()` wrappers.

Initial bootstrap failures retain the existing server error behavior. Runtime
connection failures do not discard a usable cached snapshot.

Authoritative server refresh is non-destructive when the active channel remains
present: drafts, thread state, split layout, history scope, selection, viewport,
and active channel are retained while sidebar and identity metadata update. If
the channel was removed, refresh chooses an available fallback without
rebuilding the window tree or discarding the compose draft. Realtime connection
state is server-scoped, and cached usable servers remain switchable while
offline or reconnecting.

## Testing

`internal/service/connection_test.go` uses scripted socket attempts, a fake
waiter, and deterministic jitter to verify:

- first-connect state ordering;
- reconnect state ordering and reconciliation timing;
- exponential growth, jitter bounds, and the 30-second cap;
- attempt reset after successful authentication;
- cancellation during socket activity and during backoff;
- reconciliation failure isolation;
- independent managers for independent servers.

Mattermost client tests verify that readiness is emitted only after the
authentication write succeeds and never for dial/authentication failures.

`cmd/mmk` tests verify authoritative snapshot replacement, active-channel-only
history reconciliation, inactive-server behavior, cache-before-UI ordering,
post-ID deduplication, stale-generation suppression, per-server lock
independence, dependency validation, post-commit history failures, server
isolation, shutdown waiting, and secret-safe errors.
