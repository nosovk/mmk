// Package ids declares named string types for the domain
// identifiers that flow across mmk's module boundaries.
//
// Phase 7 of the SOLID refactor of internal/ui/app.go. The
// motivation is the bug class where two "just a string"
// identifiers swap positions in a function signature -- e.g.
// passing a teamID where a channelID is expected, or a threadTS
// where a messageTS is expected. With every ID typed as plain
// string, the compiler accepts the swap silently; the bug
// surfaces only when the resulting Slack API call returns
// channel_not_found or the cache lookup misses.
//
// SCOPE (per the "smallest scope" decision in the Phase 7
// kickoff): these types are used at the seams we built in
// Phases 2-6 -- the Phase 3 service interfaces (ReactionService,
// MessageService, ThreadService, ChannelService) and the
// XxxFunc callback types in internal/ui/callbacks.go that flow
// closures into the service adapters. Internal-to-package
// string fields on App and inside the cache / slack / cmd/mmk
// packages stay as `string`. Conversions happen at the boundary:
// App callsites pass `ids.ChannelID(a.activeChannelID)`;
// cmd/mmk closures receive typed parameters and convert back to
// `string` for SQLite / HTTP serialization.
//
// Named string types serialize transparently with encoding/json
// and lipgloss, so no Marshaler/Unmarshaler implementations are
// needed.
//
// The types deliberately have no methods. They're values, not
// behavior. fmt.Sprintf, log statements, and map keys all work
// against them unchanged because every operation that needs a
// plain string is reachable via `string(id)`.
package ids

// ServerID identifies a configured Mattermost server. It is deliberately
// distinct from TeamID because a Mattermost server can contain many teams.
type ServerID string

// ChannelID is a Slack channel identifier (e.g. "C0123ABCD" for
// public/private channels, "D0123ABCD" for DMs, "G0123ABCD" for
// MPIMs). Used everywhere a channel is referenced by ID rather
// than display name.
type ChannelID string

// TeamID is a Slack workspace identifier (e.g. "T0123ABCD").
// Used to scope channels, users, sections, presence, threads,
// and the workspace rail / finder.
type TeamID string

// ThreadTS is a Slack message timestamp identifying the parent
// of a thread (formatted as "1234567890.123456"). Distinct from
// MessageTS because a threadTS is always the parent and is
// stable for the life of the thread, while a MessageTS may be
// any reply within a thread.
type ThreadTS string

// MessageTS is a Slack message timestamp (formatted as
// "1234567890.123456"). Identifies any individual message --
// channel-level or thread-reply. Distinct from ThreadTS to
// catch the common bug where the two are swapped (e.g. marking
// a reply as the thread parent).
type MessageTS string

// UserID is a Slack user identifier (e.g. "U0123ABCD" for
// regular users, "USLACKBOT" for the platform bot, "B0123ABCD"
// for bot apps in some response shapes). Used for presence,
// mention resolution, self-send dedup, and avatar fetching.
type UserID string
