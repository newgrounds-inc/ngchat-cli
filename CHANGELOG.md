# Changelog

All notable changes to ngchat are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Site login: `ngchat login`, plus a prompt on the first run when
  nothing is stored (`ngchat logout` is unchanged). Takes a username or
  email address,
  the password, and a two-factor code; a TOTP recovery code works at the
  same prompt. Three wrong codes restart from the password, and an empty
  password returns to the identity prompt without a request.
- In-place chat-token renewal: the client answers the server's
  `revalidate` nudge with one re-mint and a `reauthenticate`, so an
  evening's session no longer drops every hour ([#1]).
- Roster: user count in the status bar and `/who` listing names, with
  moderators marked and away users dimmed with their away message.
- Mention and DM highlight with a terminal bell; `-quiet` keeps the
  highlight and drops the bell. A backfilled mention never rings.
- OSC 8 hyperlinks for links and mentions, with the visible URL kept
  for terminals without hyperlink support.
- `ctrl+t` toggles server timestamps, shown in the local zone.
- `away` messages and the subscribe notifications inbox render as faint
  event rows.
- `-debug` writes redacted frames and state transitions to a `0600` log
  in the per-user state directory; the path is printed on exit.
- The server's entry-gate notice (not a supporter, under 18, e-mail not
  validated, banned) and a refused subscribe are shown on exit instead
  of a blank screen.
- CI on every pull request and push to `main` (vet, race tests on
  Linux, macOS and best-effort Windows, `go mod tidy` drift, GoReleaser
  snapshot build); the release workflow runs the Linux and macOS tests
  before publishing. Renovate for Go modules and Action pins, and a
  security policy.

### Changed

- Chat tokens are minted through the site's
  `POST /api/v1/auth/service-token` with a per-run cookie jar and the
  XSRF token read from the jar before every request; `jwt.php` is no
  longer used (ADR 0003).
- The credential store holds only the `ng_remember` cookie value. The
  v0.1 full-cookie-header entry is deleted on sight and the user logs
  in once.
- A 401 from the site while minting means the cookie was revoked or the
  password changed: the client stops and asks for `ngchat login`.
- `esc` no longer quits; `ctrl+c` does.
- The transcript is capped at 2000 rows.
- One channel, `general`; the `-channel` flag is gone.
- Release archives are built with `-trimpath` and a fixed modification
  time, and GitHub Actions are pinned to commit SHAs.
- The terminal stack moved to Bubble Tea, Bubbles and Lip Gloss v2
  (`charm.land`). Colors are now downsampled for the terminal at
  output time, so an entry-gate notice printed on exit renders the
  same way inside and outside the TUI.

### Removed

- Password login for allowlisted bots (`-user` and the password
  minter) and the `set-cookie` subcommand. `NGCHAT_NG_COOKIE` remains
  for development and the smoke harness.

### Fixed

- The `kicked` frame's reason was read from the wrong field; kicks and
  idle timeouts now report the server's reason.
- A refused subscribe after a successful authenticate is treated as
  access denied instead of leaving the client idle.
- Mentions render as `@name` with an https link instead of the raw
  markup.

## [0.1.0] - 2026-08-13

### Added

- Connect to Newgrounds Chat over WebSocket and join one channel.
- Render the server's HTML messages to ANSI, with spoilers hidden until
  `ctrl+s`, emotes as `:code:`, and unknown tags degraded to text.
- Typing indicators, join and part rows, and DMs.
- Cookie stored in the OS keyring with a `0600` file fallback.
- Password login for allowlisted bots; `ngchat logout` and a
  `set-cookie` subcommand.
- `-version`.
- GoReleaser builds for Linux, macOS, and Windows on `v*` tags.

[Unreleased]: https://github.com/newgrounds-inc/ngchat-cli/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/newgrounds-inc/ngchat-cli/releases/tag/v0.1.0
[#1]: https://github.com/newgrounds-inc/ngchat-cli/issues/1
