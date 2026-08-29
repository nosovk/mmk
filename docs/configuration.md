# Configuration

`mmk` uses XDG-style configuration and data directories. Server identity and
credentials are intentionally separate from UI preferences.

## Paths

Default locations:

| Purpose | Default path |
| --- | --- |
| UI preferences | `~/.config/mmk/config.toml` |
| Mattermost server registry | `~/.config/mmk/servers.toml` |
| Onboarding lock | `~/.config/mmk/servers.lock` |
| Custom themes | `~/.config/mmk/themes/` |
| SQLite cache | `~/.local/share/mmk/cache.db` |

`XDG_CONFIG_HOME` replaces `~/.config`, and `XDG_DATA_HOME` replaces
`~/.local/share`. For example, with `XDG_CONFIG_HOME=/tmp/config`, the server
registry is `/tmp/config/mmk/servers.toml`.

The debug log is different: when `MMK_DEBUG` is set, `mmk-debug.log` is created
and truncated in the current working directory.

## Add Or Update A Server

Run:

```bash
mmk --add-server
```

The interactive form asks for:

- **Mattermost server URL:** the deployment root or a URL ending in `/api/v4`
- **Personal access token:** entered through a masked field
- **Display name:** optional label for the server rail

Accepted examples include:

```text
https://chat.example.com
https://chat.example.com/api/v4
https://example.com/mattermost
https://example.com/mattermost/api/v4
```

The URL must use HTTP or HTTPS, include a host, and contain no embedded
credentials, query, or fragment. The `/api/v4` suffix is removed before the URL
is stored. Default ports are normalized, while deployment subpaths remain part
of the server identity.

Before writing anything, `mmk` uses the PAT to fetch the authenticated user and
that user's teams. Empty, invalid, or unauthorized tokens are rejected.

Repeating `mmk --add-server` with the same canonical server URL updates the
existing registry entry and credential. A different canonical URL appends a new
server. Registry order controls server-rail and cache-hydration order.

## Personal Access Tokens

Mattermost PATs authenticate REST and WebSocket requests with the full access of
the associated user account.

To create one:

1. A system administrator enables **Personal Access Tokens** under **System
   Console > Integrations > Integration Management**. Mattermost disables this
   setting by default.
2. If required by the server's permissions, the administrator grants the user
   permission to create PATs.
3. The user opens **Profile > Security > Personal Access Tokens** (or **User
   Settings > Security** in some Mattermost versions), creates a token with a
   descriptive name, and records the displayed value.

Treat a PAT like a password. Do not place it in `config.toml`, `servers.toml`, a
shell history, an issue, or a debug log. Revoke the token in Mattermost if it is
exposed.

## Credential Storage

PATs are never serialized into the TOML registry. They are stored under service
name `mmk` and an account derived from the canonical server ID:

- Linux: Secret Service, normally backed by GNOME Keyring, KWallet, or another desktop keyring
- macOS: Keychain Services
- Windows: Credential Manager as a generic credential

`mmk` has no plaintext fallback. If setup or startup reports that the system
credential store is unavailable, unlock the keyring and ensure a desktop
session is available. A macOS binary built with `CGO_ENABLED=0` cannot access
Keychain Services; official macOS releases are built with cgo enabled.

The onboarding transaction validates first, serializes concurrent updates with
`servers.lock`, updates the credential, and atomically replaces `servers.toml`.
It attempts to restore the previous credential if a pre-commit registry write
fails and no concurrent credential change occurred.

## Server Registry

`servers.toml` is owned by `mmk`. Do not put tokens or unrelated keys in it.
The decoder rejects unknown fields, unsupported versions, duplicate IDs, missing
required identity fields, and symlinked registry paths. New files use mode
`0600`.

Illustrative structure:

```toml
version = 1

[[servers]]
id = "chat-example-com-<stable-hash>"
url = "https://chat.example.com"
display_name = "Work"
user_id = "mattermost-user-id"
username = "alice"
```

Normally, use `mmk --add-server` rather than editing this file. IDs and user
metadata are generated and validated by onboarding.

## Multiple Servers And Teams

Each `[[servers]]` entry is one Mattermost deployment and authenticated account.
At startup, `mmk` reads each PAT independently, hydrates cached state in registry
order, then starts independent live bootstrap and reconnect workers.

All teams returned for the authenticated user are grouped beneath that server.
Teams do not require separate credentials or separate `--add-server` runs.
Direct and group-message channels are also associated with the server and shown
alongside team channels.

Use the server rail, digit keys `1` through `9`, or the workspace/server picker
to switch between configured servers. A server may remain usable from cache
while its live connection is offline.

## UI Preferences

`config.toml` is optional. If it does not exist, defaults are used.

The current Mattermost startup directly wires these settings:

```toml
[appearance]
theme = "nord"
mouse_wheel_lines = 3

[theme]
primary = ""
accent = ""
warning = ""
error = ""
background = ""
surface = ""
surface_dark = ""
text = ""
text_muted = ""
border = ""
```

- `appearance.theme` selects a built-in or loaded custom theme.
- `appearance.mouse_wheel_lines` controls scroll distance and is clamped to at least `1`; invalid or unset values fall back to `3`.
- Non-empty `[theme]` colors override fields in the selected theme.
- Additional `.toml` themes can be placed in `~/.config/mmk/themes/`.

The parser still accepts a wider upstream schema for animations, notifications,
cache limits, image behavior, sidebar sections, and per-workspace preferences.
Those values are not currently wired by the Mattermost production startup and
should not be relied on until their Mattermost behavior is implemented and
documented. In particular, desktop notifications and Mattermost attachment
rendering are currently disabled even if their legacy configuration fields are
set.

## Troubleshooting

**No servers configured**

```text
no Mattermost servers configured; run mmk --add-server first
```

Run onboarding and complete all three prompts.

**Credential store unavailable**

Unlock the OS keyring, start a desktop session, and retry. On headless Linux,
configure a compatible Secret Service implementation; `mmk` will not write the
PAT to a file instead.

**Token rejected**

Confirm the PAT is enabled, belongs to the intended user, has not been revoked,
and can access at least the current-user and team APIs. Re-run `mmk --add-server`
to replace a rotated token.

**Wrong deployment URL**

Use the same root users open in a browser, including any deployment subpath. Do
not append endpoints beyond the optional `/api/v4` suffix.
