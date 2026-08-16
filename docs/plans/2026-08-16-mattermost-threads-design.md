# Mattermost Threads Design

**Goal:** Support opening Mattermost threads from channel history, loading their root and replies, sending replies, and applying realtime replies without enabling the Slack-style workspace Threads view.

## Scope

Mattermost servers gain the existing channel-level thread panel and reply composer. Users can open a root post or a reply, see cached content immediately, refresh from Mattermost, send a reply with `root_id`, and receive realtime updates in the matching open thread.

The workspace-wide Threads sidebar remains disabled for Mattermost. Thread subscriptions, thread marking, and a Mattermost equivalent of Slack's involved-thread list are outside Task 14.

## Architecture

The implementation reuses `internal/ui/thread.Model` and the existing cache-first `ThreadService` orchestration. Provider-neutral message identity comes from `messages.MessageItem.MessageID()` and `RootMessageID()` rather than direct Slack timestamp fields.

`internal/mattermost.Client` adds a post-thread operation for `GET /api/v4/posts/{post_id}/thread`. It validates the `order` and `posts` envelope using the same bounded-response and identity rules as channel history. `CreatePostRequest` gains an optional `RootID`, serialized as `root_id` for replies.

A dedicated Mattermost thread service reads `ListMattermostThreadPosts`, fetches the authoritative thread, resolves unknown users, and writes posts and users through the existing context-aware history transaction. It returns root-first chronological presentation data that the command adapter converts to `messages.MessageItem`.

Mattermost runtime wiring installs cache-read, live-fetch, and reply-send collaborators in the existing thread UI service. The Slack workspace Threads list remains unavailable.

## Data Flow

Opening a selected post derives the root from `RootMessageID()`, falling back to `MessageID()`. The UI opens the panel, reads cached root and replies synchronously, then starts an authoritative fetch. A stale result is rejected by the existing server/channel/generation scope.

The live fetch validates that the requested root exists, all messages share the requested channel, and every non-root post references the requested root. The service resolves authors and atomically persists the returned posts and users before publishing replies to the UI. Root-only success produces an empty non-nil reply slice; failure produces nil replies so cached content remains visible.

Reply composition creates a Mattermost optimistic item identified by correlation ID and carrying `RootID`. The POST response replaces it with the authoritative message. If the WebSocket event arrives first, correlation-aware upsert collapses the optimistic and authoritative rows instead of adding a duplicate.

A realtime `posted` event with `RootID` is persisted before notification. If its server, channel, and root match the open thread, the reply is upserted into the panel and the root reply count is updated. Replies for other threads remain cached but do not alter the open panel.

## Errors And Cancellation

Invalid request IDs fail before HTTP. Malformed thread envelopes, missing roots, cross-channel posts, mismatched reply roots, and nonpositive creation timestamps fail closed. Context cancellation and deadlines propagate through HTTP, user lookup, and SQLite writes.

Ordinary user lookup failures retain the post with user-ID fallback, matching channel history. A canceled lookup aborts persistence. Failed sends remove only the matching optimistic reply and preserve the open panel and composer error behavior.

## Testing

TDD coverage is split across the client, service, thread model, UI reducers, and command wiring. Tests cover endpoint shape, root extraction, authoritative order, validation, `root_id`, cache persistence and enrichment, provider-neutral opening, optimistic replacement, HTTP/WebSocket ordering, realtime routing, stale-scope rejection, and the disabled workspace Threads view.

Final verification runs focused thread/reply tests, the full suite, race tests for touched packages, `go vet`, `go build`, and `git diff --check`.
