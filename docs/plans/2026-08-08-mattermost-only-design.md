# mmk Mattermost-Only Fork Design

## Goal

Build `mmk`, a fast keyboard-driven Mattermost TUI derived from `gammons/slk`, while supporting Mattermost only and removing Slack-specific protocols, authentication, models, and formatting.

## Scope

The first useful release supports:

- multiple Mattermost servers;
- Personal Access Token authentication;
- multiple teams per server;
- public channels, private channels, direct messages, and group messages;
- cached message history with pagination;
- sending messages;
- realtime posts over WebSocket;
- reconnect and recovery synchronization;
- unread state and marking channels as read;
- viewing threads and posting replies;
- the existing themes, terminal image support, and Vim-style navigation.

Reactions, file upload, edit/delete, search, status controls, and desktop notifications are explicitly deferred until after this vertical slice works end to end.

## Approach

Fork the repository with history and retain the mature Bubble Tea UI, rendering, cache infrastructure, and terminal behavior. Replace the domain and transport layers with Mattermost-native models rather than creating a generic multi-provider abstraction. Slack compatibility is not a requirement.

The implementation should remove Slack code as Mattermost replacements become operational. It must not retain compatibility shims or provider interfaces that serve only a hypothetical future Slack backend.

## User Model

The workspace rail represents Mattermost servers. The sidebar for the active server groups channels under collapsible team sections. Direct and group messages appear in a server-wide section because they are not owned by a single team in the same way as ordinary channels.

```text
Server
|- Direct messages
|- Team A
|  |- Public channels
|  `- Private channels
`- Team B
   |- Public channels
   `- Private channels
```

## Architecture

```text
UI Layer           Bubble Tea models, views, key handling, themes
Service Layer      Server manager, channel bootstrap, messages, unread state
Client Layer       Mattermost REST API v4 and WebSocket client
Data Layer         SQLite cache, TOML configuration, OS secret storage
```

Target package structure:

```text
cmd/mmk/
  main.go
  onboarding.go

internal/
  mattermost/
    auth.go
    client.go
    models.go
    websocket.go
  service/
    server.go
    channels.go
    messages.go
  cache/
  config/
  ui/
  image/
  notify/
```

The client package exposes only the API operations required by the application. Mattermost SDK response types must be converted at the client boundary into compact application models.

Core models:

```go
type Server struct {
	ID     string
	Name   string
	URL    string
	UserID string
}

type Team struct {
	ID          string
	ServerID    string
	Name        string
	DisplayName string
}

type Channel struct {
	ID           string
	ServerID     string
	TeamID       string
	Name         string
	DisplayName  string
	Kind         ChannelKind
	LastViewedAt int64
}

type Message struct {
	ID        string
	ChannelID string
	UserID    string
	RootID    string
	Text      string
	CreatedAt int64
	UpdatedAt int64
}
```

Direct-message display names are derived from channel participants. Artificial display names are not written back into API models.

## Authentication

`mmk --add-server` prompts for a server URL and Personal Access Token. It normalizes the URL and validates the credentials through `GET /api/v4/users/me` before saving anything.

Configuration stores only non-secret server metadata. Tokens are stored in the operating-system credential store. If secure storage is unavailable, onboarding fails with an actionable error instead of writing a plaintext token.

## Startup And Data Flow

```text
configuration -> SQLite cache -> immediate UI
                         |
                         v
             REST bootstrap and reconciliation
                         |
                         v
                  WebSocket realtime
```

Each server has an independent connection manager. A failure on one server must not block the UI or other servers. SQLite is a cache, while Mattermost remains authoritative.

After a WebSocket reconnect, the client performs REST reconciliation because events may have been missed while disconnected.

## Realtime Events

The first slice requires realtime post creation and read/unread synchronization. The WebSocket decoder should also tolerate and model the following events incrementally:

- `posted`;
- `post_edited`;
- `post_deleted`;
- `channel_viewed`;
- `channel_updated`;
- `channel_member_updated`;
- `direct_added`;
- `user_added`;
- `user_removed`;
- `status_change`;
- `typing`.

Unknown event types are ignored and logged; they never terminate the connection.

## Sending Messages

Sending creates a local pending message immediately. A successful REST response replaces the local identity with the Mattermost post ID. The corresponding WebSocket event is deduplicated by post ID and a client correlation ID.

Failed sends remain visible with a failed state and can be retried. Persistent offline send queues are out of scope for the first release.

## Error Handling

Connection states are `Connecting`, `Connected`, `Offline`, and `Reconnecting`. Reconnect uses exponential backoff with jitter. Cached history remains available while offline, but mutation actions report that the server is unavailable.

REST errors are converted into errors containing the HTTP status, Mattermost error ID when present, and a user-readable message. Authentication errors are distinguished from transient network failures.

## Testing

The Mattermost client uses `httptest.Server` and a test WebSocket endpoint to verify authentication headers, URL normalization, response decoding, pagination, post creation, authentication challenges, event decoding, and reconnect behavior.

Service tests use small fake clients to cover multi-team grouping, direct-message names, unread state, deduplication, and reconnect reconciliation. SQLite tests run real migrations against temporary databases. Existing UI tests are retained and converted from Slack timestamps and structures to Mattermost post IDs and millisecond timestamps.

An optional real-server smoke suite is enabled through environment variables and is not required for `go test ./...`.

## Delivery Sequence

1. Rename project identity from `slk` to `mmk` while preserving attribution.
2. Add Mattermost models and REST client.
3. Add PAT onboarding and secure token storage.
4. Bootstrap users, teams, channels, and direct messages.
5. Adapt cache records and migrations.
6. Connect the rail and sidebar to servers, teams, and channels.
7. Implement history, pagination, and sending.
8. Add WebSocket events, reconnect, and reconciliation.
9. Add unread/read synchronization.
10. Add thread viewing and replies.
11. Remove remaining Slack packages and dependencies.
12. Update documentation and verify the complete application.

## Attribution

The project remains under the upstream MIT license and retains the original copyright notice. The README must clearly state that `mmk` is derived from `gammons/slk` and is an independent Mattermost client.
