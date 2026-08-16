# Mattermost Threads Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add Mattermost thread viewing, reply creation, optimistic reconciliation, and realtime reply routing while keeping the workspace-wide Threads view disabled.

**Architecture:** Extend the Mattermost client and services at narrow provider boundaries, then reuse the existing cache-first thread panel. Preserve Mattermost IDs and correlation IDs through `messages.MessageItem` so HTTP responses and WebSocket events deduplicate regardless of arrival order.

**Tech Stack:** Go, Mattermost REST/WebSocket API, SQLite cache, Bubble Tea, existing `internal/ui/thread` model.

---

### Task 1: Fetch Mattermost Post Threads

**Files:**
- Create: `internal/mattermost/post_thread_test.go`
- Modify: `internal/mattermost/client.go:386-446`

**Step 1: Write the failing endpoint and order test**

Add `TestClient_PostThreadRequestsEndpointAndReconstructsOrder`. Serve an `order` plus `posts` response containing a root and out-of-map-order replies. Assert `GET /api/v4/posts/root-1/thread`, root-first authoritative order, complete `RootID` mapping, and ignored unordered map entries.

**Step 2: Run the test to verify RED**

Run: `go test ./internal/mattermost -run '^TestClient_PostThreadRequestsEndpointAndReconstructsOrder$' -v`

Expected: FAIL because `Client.PostThread` does not exist.

**Step 3: Implement the minimal client method**

Add:

```go
func (c *Client) PostThread(ctx context.Context, rootPostID string) (MessagePage, error)
```

Validate `rootPostID`, request `posts/{escapedID}/thread`, decode `postListResponse`, deduplicate ordered IDs, map wire posts to domain messages, and require the requested root.

**Step 4: Add failing validation tests one behavior at a time**

Add tests for invalid root ID without a request, missing ordered post, missing requested root, mismatched wire ID, cross-channel posts, mismatched reply `root_id`, nonpositive `create_at`, and context cancellation. Run each test before implementing its validation and confirm the expected failure.

**Step 5: Implement minimal validation and verify GREEN**

Run: `go test ./internal/mattermost -run 'TestClient_PostThread' -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/mattermost/client.go internal/mattermost/post_thread_test.go
git commit -m "feat: fetch Mattermost threads"
```

### Task 2: Create Mattermost Replies With Root IDs

**Files:**
- Modify: `internal/mattermost/client.go:449-496,862-866`
- Modify: `internal/mattermost/create_post_test.go`
- Modify: `internal/service/mattermost_send.go`
- Modify: `internal/service/mattermost_send_test.go`

**Step 1: Write failing client tests**

Add `TestClient_CreatePostSendsRootIDForReply`, `TestClient_CreatePostOmitsRootIDForRootPost`, `TestClient_CreatePostRejectsInvalidRootIDWithoutRequest`, and `TestClient_CreatePostRejectsMismatchedAuthoritativeRootID`.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost -run 'TestClient_CreatePost.*RootID' -v`

Expected: FAIL because `CreatePostRequest.RootID` and wire `root_id` are absent.

**Step 3: Implement minimal client support**

Add optional `RootID` to the domain and wire requests. Validate nonblank supplied IDs with the existing Mattermost ID validator and require the authoritative response root to match the submitted root.

**Step 4: Write the failing service reply test**

Add `TestMattermostSendReplyForwardsRootIDAndReturnsAuthoritativeMessage`, asserting server, channel, root, text, and correlation identity.

**Step 5: Verify RED, implement, and verify GREEN**

Add `MattermostSendService.Reply` or a shared private send helper while preserving the existing root-post `Send` API.

Run: `go test ./internal/mattermost ./internal/service -run 'Test(Client_CreatePost.*RootID|MattermostSendReply)' -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/mattermost/client.go internal/mattermost/create_post_test.go internal/service/mattermost_send.go internal/service/mattermost_send_test.go
git commit -m "feat: create Mattermost thread replies"
```

### Task 3: Add Mattermost Thread Cache And Fetch Service

**Files:**
- Create: `internal/service/mattermost_threads.go`
- Create: `internal/service/mattermost_threads_test.go`
- Reuse: `internal/cache/mattermost.go:870-904,969-976`

**Step 1: Write the failing cached-read test**

Add `TestMattermostThreadReadCachedReturnsRootFirstChronologicalMessages`. Seed root and replies in nonchronological insertion order and assert root-first presentation with cached author names.

**Step 2: Verify RED**

Run: `go test ./internal/service -run '^TestMattermostThreadReadCached' -v`

Expected: FAIL because `MattermostThreadService` does not exist.

**Step 3: Implement minimal cached read**

Create narrow client/store interfaces and `NewMattermostThreadService`. Implement `ReadCached(channelID, rootID)` with `ListMattermostThreadPosts`, cached users, and the existing Mattermost display-name rules.

**Step 4: Write failing fetch tests**

Add tests that fetching persists root, replies, and newly resolved users; cached users avoid lookup; ordinary lookup failure falls back to user ID; cancellation aborts before persistence; root-only success returns a non-nil empty reply collection; invalid channel/root relationships fail.

**Step 5: Verify RED, implement fetch, and verify GREEN**

Implement `Fetch(ctx, channelID, rootID)` using `PostThread`, unknown-user lookup, `UpsertMattermostHistoryContext`, and root-first presentation.

Run: `go test ./internal/service -run '^TestMattermostThread' -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/service/mattermost_threads.go internal/service/mattermost_threads_test.go
git commit -m "feat: cache Mattermost thread history"
```

### Task 4: Make Thread UI Identity Provider-Neutral

**Files:**
- Modify: `internal/ui/app.go:1572-1625`
- Modify: `internal/ui/features.go`
- Modify: `internal/ui/reducer_workspace.go:242-253`
- Modify: `internal/ui/thread/model.go:343-425`
- Modify: `internal/ui/thread/model_test.go`
- Modify or create focused tests under: `internal/ui/`

**Step 1: Write failing open-thread identity tests**

Add tests proving a Mattermost root opens with `MessageID()` and a selected reply opens with `RootMessageID()`, without requiring Slack `TS` or `ThreadTS`.

**Step 2: Verify RED and implement provider-neutral root selection**

Run the focused tests and confirm current empty thread identity. Replace direct Slack timestamp selection in `openThreadForSelectedMessage` with provider-neutral helpers while preserving Slack behavior.

**Step 3: Write failing correlation replacement tests**

Add `TestReplaceLocalReplyMatchesCorrelationID`, `TestReplaceLocalReplyCollapsesRealtimeBeforeHTTP`, and `TestUpsertReplyDeduplicatesMattermostID` to `internal/ui/thread/model_test.go`.

**Step 4: Verify RED and implement minimal thread-model operations**

Add correlation-aware replacement/upsert behavior modeled on `messages.Model.ReplaceLocalMessage`, using `MessageID()` and `CorrelationID` while retaining existing Slack TS operations.

**Step 5: Write and pass the Mattermost feature-gate test**

Enable channel-level `FeatureThreads` for Mattermost while asserting `sidebar.SetThreadsEnabled(false)` remains in effect.

Run: `go test ./internal/ui/thread ./internal/ui -run 'Test(Thread|Reply|Mattermost.*Threads)' -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add internal/ui/app.go internal/ui/features.go internal/ui/reducer_workspace.go internal/ui/thread internal/ui/*_test.go
git commit -m "feat: enable Mattermost thread panels"
```

### Task 5: Wire Mattermost Thread Fetch And Reply Sending

**Files:**
- Modify: `cmd/mmk/mattermost_startup.go`
- Create: `cmd/mmk/mattermost_threads.go`
- Create: `cmd/mmk/mattermost_threads_test.go`
- Modify: `internal/ui/services.go`
- Modify: `internal/ui/msgs.go`
- Modify: `internal/ui/reducer_threads.go`
- Modify: `internal/ui/reducer_send.go`

**Step 1: Write failing cache-first opening tests**

Add tests proving Mattermost cached root/replies appear before fetch, live success replaces replies, root-only success clears stale replies, and fetch failure preserves cached replies.

**Step 2: Verify RED and wire cache/fetch collaborators**

Create command adapters from `MattermostThreadService` to `messages.MessageItem` and install them from `wireMattermostRuntime` through the existing `ThreadService` slots. Scope operations by active server, channel, and generation.

**Step 3: Write failing optimistic reply tests**

Add tests proving a Mattermost reply appears immediately with correlation identity and `RootID`, success replaces it with the authoritative message, failure removes only that placeholder, and stale server/channel-generation results are ignored.

**Step 4: Verify RED and implement Mattermost reply branch**

Extend the Mattermost send request with optional `RootID`. In the thread reply reducer, branch at the provider boundary, preserve Mattermost plain text, and use correlation-aware replacement.

**Step 5: Verify GREEN**

Run: `go test ./internal/ui ./cmd/mmk -run 'TestMattermost(Thread|Reply)' -v`

Expected: PASS.

**Step 6: Commit**

```bash
git add cmd/mmk/mattermost_startup.go cmd/mmk/mattermost_threads.go cmd/mmk/mattermost_threads_test.go internal/ui/services.go internal/ui/msgs.go internal/ui/reducer_threads.go internal/ui/reducer_send.go
git commit -m "feat: wire Mattermost thread replies"
```

### Task 6: Route Realtime Mattermost Replies

**Files:**
- Modify: `cmd/mmk/mattermost_events.go`
- Modify: `cmd/mmk/mattermost_events_test.go`
- Modify: `internal/ui/msgs.go`
- Modify: `internal/ui/reducer_send.go`
- Modify or create focused tests under: `internal/ui/`

**Step 1: Write failing realtime routing tests**

Cover matching open thread, different root, different server/channel, root post exclusion, root reply-count update, persistence-before-notification, realtime-before-HTTP, and HTTP-before-realtime.

**Step 2: Verify RED**

Run: `go test ./cmd/mmk ./internal/ui -run 'TestMattermostRealtimeReply' -v`

Expected: FAIL because posted events only trigger channel-history refresh.

**Step 3: Implement minimal realtime message and reducer path**

After successful persistence, emit a scoped Mattermost post message carrying the converted authoritative `MessageItem`. Upsert only replies matching the open thread, collapse optimistic rows by correlation ID, and retain the existing channel-history refresh.

**Step 4: Verify GREEN**

Run: `go test ./cmd/mmk ./internal/ui -run 'TestMattermostRealtimeReply' -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add cmd/mmk/mattermost_events.go cmd/mmk/mattermost_events_test.go internal/ui/msgs.go internal/ui/reducer_send.go internal/ui/*_test.go
git commit -m "feat: route Mattermost realtime replies"
```

### Task 7: Full Verification And Task Commit

**Files:**
- Modify as required by failing verification only.

**Step 1: Run focused tests**

```bash
go test ./internal/mattermost ./internal/service ./internal/ui/thread ./cmd/mmk -run 'Test(Thread|Reply|MattermostThread|MattermostRealtimeReply)' -v
```

Expected: PASS.

**Step 2: Run full and race tests**

```bash
go test ./...
go test -race ./internal/mattermost ./internal/service ./internal/ui/thread ./internal/ui ./cmd/mmk
```

Expected: PASS.

**Step 3: Run static and build verification**

```bash
go vet ./...
go build ./...
git diff --check
```

Expected: PASS.

**Step 4: Review intended changes**

```bash
git status --short --branch
git diff
git log --oneline -10
```

Confirm the workspace-wide Mattermost Threads list remains disabled and no Task 15 Slack-removal work leaked into this task.

**Step 5: Create the final Task 14 commit if prior task commits were intentionally squashed**

```bash
git add internal/mattermost internal/service internal/ui/thread internal/ui cmd/mmk docs/plans
git commit -m "feat: support Mattermost threads"
```
