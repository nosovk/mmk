# Setup

mmk reads your session directly from the **Slack desktop app** — no Slack App,
no admin approval, no OAuth flow, and no tokens to copy. The only requirement is
that the Slack desktop app is installed and you're signed in to it.

## 1. Sign in to the Slack desktop app

Install the Slack desktop app if you haven't already, and sign in to each
workspace you want to use in mmk.

## 2. Add your workspaces

```bash
mmk --add-workspace
```

Or just run `mmk`. Onboarding launches automatically when no workspaces are
configured.

mmk detects the workspaces you're signed in to in the desktop app and shows
them in a list. Select the ones you want (all are selected by default) and
you're done.

## Removing a workspace

```bash
mmk --remove-workspace
```

Interactive picker. This deletes the saved token from
`~/.local/share/mmk/tokens/`; your `config.toml` and SQLite cache are left
untouched.

## Multiple workspaces

You can add as many workspaces as you like by running `mmk --add-workspace`
again. They all stay connected in parallel for live unread badges. Use
`:ws` for the picker, or `1`–`9` to jump directly. Configure rail order
and per-workspace settings in [[Configuration]].

## Token expiry

You don't need to do anything when a token expires. mmk re-mints tokens
automatically from the Slack desktop app on each launch (and mid-session if
needed), so sessions stay fresh on their own.

If you ever sign out of the desktop app, just sign back in — mmk will pick the
session back up the next time it needs to re-mint. See the auth caveat in
[[Tradeoffs and Non-Goals|Tradeoffs-and-Non-Goals]].
