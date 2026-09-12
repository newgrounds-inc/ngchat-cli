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
- OSC 8 hyperlinks for links and mentions; a labelled link shows only
  its label, as on the web.
- `ctrl+t` toggles server timestamps, shown in the local zone.
- While scrolled up, the help row becomes a `more messages below`
  banner and `end` jumps back; typing or sending a message jumps back
  too, as on the web.
- `away` messages, `/me`, `/slap` and the subscribe notifications inbox
  render as faint event rows, worded as the server sends them.
- `-debug` writes redacted frames and state transitions to a `0600` log
  in the per-user state directory; the path is printed on exit.
- The server's entry-gate notice (not a supporter, under 18, e-mail not
  validated, banned) and a refused subscribe are shown on exit instead
  of a blank screen.
- Typing `@` opens a completion list from the room's live user list:
  `tab`/`shift+tab` or the up/down arrows cycle it, `enter` accepts and
  `esc` closes; a window too short for the list previews the
  highlighted candidate in the input line instead. `/` as the first
  character completes slash commands, rank-filtered to what the viewer
  may use and refreshed on a mid-session rank change; aliases match and
  `*` wildcards, but the canonical name is always what gets inserted.
  Starting a word with `ng…`/`tf…` completes NG "dank meme" emote
  shortcodes from an embedded list generated from the site's own
  emoticon data. Typing `:shortname` completes emoji from an embedded
  catalog generated the same way, each row showing the glyph next to
  its shortname.
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
- `ngchat logout` logs the device out on the site
  (`POST /api/v1/auth/logout`) before clearing the stored cookie, so a
  copy kept elsewhere stops minting; it exits non-zero when the site
  could not be reached or the OS keyring could not be checked.
- One stored login per site: the production cookie keeps its slot and
  any other `NGCHAT_SITE_URL` gets its own, so switching to a dev stack
  never sends the production cookie there (ADR 0004). Both
  `NGCHAT_SITE_URL` and `NGCHAT_WS_URL` must be `https`/`wss` off
  loopback, the chat host must be on the site's domain, and the site
  client never follows a redirect.
- The credential store reads its fallback file before the OS keyring,
  and reports a keyring that gave no answer instead of treating it as
  empty; the file is rewritten atomically so its `0600` mode is
  restored on every save.
- The build requires Go 1.26.8 and CI and the release gate run
  `govulncheck`.
- The transcript is capped at 2000 rows.
- One channel, `general`; the `-channel` flag is gone.
- Release archives are built with `-trimpath` and a fixed modification
  time, and GitHub Actions are pinned to commit SHAs.
- The terminal stack moved to Bubble Tea, Bubbles and Lip Gloss v2
  (`charm.land`). Colors are now downsampled for the terminal at
  output time, so an entry-gate notice printed on exit renders the
  same way inside and outside the TUI.
- The smoke harness reads `NGCHAT_NG_COOKIE` like the TUI;
  `SMOKE_NG_COOKIE` is gone and operators who exported it must rename
  it. Smoke also defaults to the production endpoints when the URL
  variables are unset, runs the credential-store migration, exits 2
  with `run ngchat login` instead of failing on a missing login, and
  reports a locked keyring as such. Both binaries share the one
  "prepare a run" step in `internal/run` (ADR 0008).

### Removed

- Password login for allowlisted bots (`-user` and the password
  minter) and the `set-cookie` subcommand. `NGCHAT_NG_COOKIE` remains
  for development and the smoke harness.

### Fixed

- Text from the network (messages, usernames, away messages, close
  reasons, site error text) is stripped of terminal control characters
  and bidi overrides before it reaches the screen, and only http(s)
  targets become OSC 8 links; a mention link hides its target only when
  it really is that user's page.
- The WebSocket read limit is 64 MiB instead of the library's 32 KiB, so
  a `subscribed` envelope with a few long messages in its backfill no
  longer drops the connection on every join.
- The `-debug` log is recreated with mode `0600` on every run instead of
  inheriting a wider mode, and is never written through a symlink.
- The `kicked` frame's reason was read from the wrong field; kicks and
  idle timeouts now report the server's reason.
- A refused subscribe after a successful authenticate is treated as
  access denied instead of leaving the client idle.
- Mentions render as `@name` with an https link instead of the raw
  markup.
- `ctrl+s`, `ctrl+t` and a resize keep the reader's place in the
  transcript instead of jumping to the bottom.
- A terminal that shrinks while the reader is at the bottom keeps them
  there instead of leaving them scrolled up under the `more messages
  below` banner.

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
