# mmk

> **A fast, keyboard-driven Mattermost TUI.**
> One terminal application for channels, direct messages, threads, and multiple Mattermost servers.

`mmk` is an independent, unofficial Mattermost client written in Go with
[Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss). It is derived from
[gammons/slk](https://github.com/gammons/slk); the mature terminal UI and project
history were retained while the runtime, authentication, models, cache, and
formatting were migrated to Mattermost.

## Current MVP

- Multiple Mattermost servers connected independently, with all teams grouped under each server
- Cache-first startup and channel history, including older-history pagination
- Public channels, private channels, direct messages, and group messages
- Realtime new posts over authenticated WebSockets
- Message sending with optimistic pending state and server-event reconciliation
- Thread viewing and replies, including realtime thread replies
- Unread indicators and Mattermost channel-view read synchronization
- Automatic reconnect with per-server state and active-channel reconciliation
- Vim-inspired modal navigation, fuzzy local channel finder, built-in themes, and emoji autocomplete
- SQLite-backed offline scrollback when live requests are unavailable

The current Mattermost runtime deliberately does not enable reactions, file
uploads, message edit/delete, message search, status controls, typing indicators,
desktop notifications, new DM creation, message permalinks, or the
workspace-wide Threads view. See [Implementation Status](docs/STATUS.md) for the
authoritative coverage and limitations.

## Install

**Homebrew** (macOS):

```bash
brew install nosovk/tap/mmk
```

**Go:**

```bash
go install -ldflags="-s -w" -trimpath github.com/nosovk/mmk/cmd/mmk@latest
```

**Build from source:**

```bash
git clone https://github.com/nosovk/mmk.git
cd mmk
go build -o bin/mmk ./cmd/mmk
```

Releases also provide Linux and macOS `.tar.gz` archives, a Windows `.zip`,
checksums, and Linux `.deb`, `.rpm`, and `.apk` packages.

## Mattermost Setup

`mmk` authenticates with a Mattermost Personal Access Token (PAT). The token has
the same access as its Mattermost account, so create a dedicated token, do not
share it, and revoke it if it is exposed.

1. Ask a Mattermost system administrator to enable personal access tokens under
   **System Console > Integrations > Integration Management** if the option is
   unavailable. Mattermost disables PAT creation by default, and an administrator
   may also need to grant your account permission to create one.
2. In Mattermost, open **Profile > Security > Personal Access Tokens** (shown as
   **User Settings > Security** in some versions), create a token with a useful
   description, and record the token when it is displayed.
3. Run the masked interactive setup:

```bash
mmk --add-server
```

Enter the deployment root, such as `https://chat.example.com`, or a URL ending in
`/api/v4`. Deployment subpaths such as `https://example.com/mattermost` are also
supported. `mmk` validates the token and team access before saving the server.

The PAT is stored in the operating system credential store, not in TOML:

- Linux: Secret Service through the desktop keyring
- macOS: Keychain Services
- Windows: Credential Manager

The non-secret server registry is stored at `~/.config/mmk/servers.toml` by
default. If the credential store is locked or unavailable, unlock it and ensure
you are running inside a desktop session. There is no plaintext-token fallback.

Run `mmk --add-server` again with the same server URL to update that server's PAT
or display name. Run it with another URL to add another server. Every team the
authenticated account belongs to appears under its server; you do not add teams
separately.

Start the client with:

```bash
mmk
```

Full path, registry, and configuration details are in
[docs/configuration.md](docs/configuration.md).

## Terminal Notes

The current Mattermost vertical slice renders text messages and does not wire
Mattermost file attachments into the retained inline-image UI. Terminal image
protocol settings therefore do not currently affect Mattermost messages.

`mmk` updates the terminal title with unread state. Inside tmux, forward the
active pane title with:

```tmux
set -g set-titles on
set -g set-titles-string '#T'
```

## Debugging

Set `MMK_DEBUG=1` to write `mmk-debug.log` in the current working directory:

```bash
MMK_DEBUG=1 mmk
```

The file is truncated on each run. Debug logging redacts configured Mattermost
tokens from handled client errors, but logs can still contain server, channel,
user, and message metadata. Review a log before sharing it.

## Documentation

- [Configuration and credentials](docs/configuration.md)
- [Development and verification](docs/development.md)
- [Implementation status and limitations](docs/STATUS.md)
- [Mattermost-only design](docs/plans/2026-08-08-mattermost-only-design.md)

## Contributing

Contributions are welcome. For large features, open an issue before beginning an
implementation. Every change should be understood by its author and pass:

```bash
go test ./...
go vet ./...
go build ./cmd/mmk
```

See [docs/development.md](docs/development.md) for repository layout, focused
test commands, race testing, and release checks.

### Release Hosting

The release workflow assumes this repository is hosted at
`github.com/nosovk/mmk`. In that repository, GitHub's automatically generated,
repository-scoped `GITHUB_TOKEN` can publish releases. A workflow running in
another repository, including the upstream `gammons/slk`, cannot publish to
`nosovk/mmk` with that repository's token. Cross-repository publishing requires
a separate PAT or GitHub App credential with access to `nosovk/mmk`, plus a
workflow change that uses it.

## Disclaimer

`mmk` is an independent, unofficial project. It is not affiliated with,
endorsed by, or sponsored by Mattermost, Inc.

## License And Attribution

[MIT](LICENSE) © Grant Ammons.

`mmk` is derived from [gammons/slk](https://github.com/gammons/slk) and retains
the upstream license and copyright notice.
