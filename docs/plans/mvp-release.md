# MVP release plan (public launch, v1.0.0)

Status: agreed 2026-09-02
Owner: Brendon C.
Related: ADR 0001, ADR 0002, `docs/site-login-endpoints.md` (site
contract), ngchat-cli#1, ngchat-cli#2, `newgrounds-inc/ngchat`
`docs/plans/transparent-reauth-and-service-token.md`

## Where things stood on 2026-09-02

`v0.1.0` (2026-08-13) is the scaffold: connect, one channel, HTML
rendering, spoilers, typing, join/part, DMs, keyring cookie store,
password login for allowlisted bots, GoReleaser on tag. Build, vet, and
all package tests pass on `main`.

Since then the server and site moved and this repo did not:

- `ngchat` added in-place token renewal (`revalidate` →
  `reauthenticate` → `revalidated`) and a silent roster patch
  (`userUpdated`). Tracked as ngchat-cli#1. Without it this client still
  hard-drops every hour and hides it with backfill dedupe.
- `ngchat` added optional `userID` on `playSoundboardTrigger` and
  `triggerId` on the `/sfx` `meMessage` (ngchat-cli#2). No action.
- The site's `POST /api/v1/auth/service-token` is live and the SPA no
  longer uses `GET /ngapps/jwt.php`. `jwt.php` deletion is an open item
  upstream. This client's only mint path goes through it.
- The site has no JSON login. `POST /login` and `POST /login/two-factor`
  are redirect-only.
- No CI on push or PR; only the tag-triggered release workflow.
- Repo is private.

## Definition of done

A Newgrounds supporter installs a binary, runs `ngchat`, logs in once
with username or email, password, and a 2FA code, and never logs in
again unless they change their password. They sit in `#general` for an
evening, see who is there, get pinged on mentions, and never notice a
token renewal. The release is cut by tag from CI with a changelog, and
the repo is public.

## Decisions (the grill, 2026-09-02)

**Product.** Single channel (`general`); no channel switching, the
`-channel` flag is removed. Public launch is `v1.0.0`; `v0.x` until
then. GitHub Releases plus `go install`. Linux and macOS verified,
Windows best-effort with a README note. Repo goes public when the site
login is live and end-to-end smoke passes.

**Login.** Auto-prompt on first run when no credential is stored, plus
explicit `ngchat login` and `ngchat logout`. Identifier is username or
email. After the password the site either emails a code (no
authenticator) or expects a TOTP code. Three wrong codes restart from
the password step. No resend. Always `remember=true`. Passwordless
email-only accounts are out of scope and get a clear message.
`PasswordMinter`, the `-user` flag, and the `set-cookie` subcommand are
deleted. `NGCHAT_NG_COOKIE` (full cookie header) stays as the dev and
smoke override.

**Persistence.** The keyring (or the `0600` file fallback) holds only
the `ng_remember` value. The site session and `XSRF-TOKEN` cookies live
in an in-memory jar per run; each run creates a fresh site session row,
pruned after a day. `ng_remember` is a 400-day cookie backed by a
`users_tokens` row that expires after two years idle. Nothing revokes it
but CLI logout or a password change (which logs out every device), so a
401 from the site means "revoked or password changed": the TUI shows
signed out, exits, and prints `run ngchat login`. No inline re-login.

**Re-mint.** `POST /api/v1/auth/service-token` with the cookie jar and
the `XSRF-TOKEN` cookie decoded into `X-XSRF-TOKEN`. `jwt.php` is gone
from the CLI. The site's `service_token` limiter (10/min per user)
counts before auth and CSRF, so a retry loop would also lock the user's
browser out of chat: one mint per `revalidate`, exponential backoff on
reconnect, no other retries. Cookie-based auth skips the login pipeline,
so hourly re-mints never trigger the new-IP email challenge.

**Site contract first.** Three new endpoints under `/api/v1/auth/`,
JSend, specified in `docs/site-login-endpoints.md`: `GET csrf`, `POST
login`, `POST two-factor`. The CLI is built against `httptest` fakes
from that contract while the site work runs in parallel.

**UI.** User count in the status bar and `/who` listing names, mods
marked, away users dimmed with their away message. Mention highlight
plus terminal bell, `-quiet` silences the bell. URLs become OSC 8
hyperlinks; emotes stay `:code:`. Timestamps toggle on a key. `esc` no
longer quits. Transcript capped at 2000 rows. `away` messages and the
subscribe `notifications` array render as faint event rows. `-debug`
writes redacted frames and state transitions to a fixed state-directory
path, printed on exit.

**Hygiene.** GitHub Actions for vet, test, and race on push and PR.
Renovate for Go modules and Actions pins. `SECURITY.md` with a contact
and a short disclosure policy. README rewritten for the real login
story. `CHANGELOG.md`.

## Progress

Tick as each item lands. A phase is done when its box and all children
are ticked and `go vet ./... && go test -race ./...` passes.

- [ ] **Phase 0 — contract**
  - [ ] `docs/site-login-endpoints.md` carries the request and response
        contract for `csrf`, `login`, `two-factor`, and the CLI's use of
        `service-token`
  - [ ] Site session opened from the brief (site repo, not here)

- [ ] **Phase 1 — protocol sync (ngchat-cli#1)**
  - [ ] Port `Revalidate`, `Revalidated`, `UserUpdated` into
        `internal/protocol/server.go`; `Reauthenticate` into `client.go`
  - [ ] Client answers `revalidate` once: mint, send `reauthenticate`;
        on `revalidated` apply the privilege flags; a failed mint is an
        event, never fatal (the close timer still arms the reconnect)
  - [ ] Fix `Kicked`: schema field is `reason`, we decode `message`
  - [ ] Decode `IdleTimeout.reason` into the stop error
  - [ ] Tests: decode cases for the four names; a client test driving
        `revalidate` through a fake server, asserting `reauthenticate`
        goes out and no reconnect happens
  - [ ] Live check with `cmd/smoke` against dev with the site's
        `APP_JWT_CHAT_TTL` at ~90s: renewal with no socket drop
  - [ ] Close ngchat-cli#1 and ngchat-cli#2

- [ ] **Phase 2 — login and re-mint (CLI side, against fakes)**
  - [ ] ADR 0003: service-token re-mint with a cookie jar, `jwt.php`
        removed; what the keyring holds and why; the 401 = signed-out
        rule
  - [ ] `internal/auth`: cookie jar, `csrf` priming, `login` and
        `two-factor` flow with the three-attempt rule, `ServiceTokenMinter`
  - [ ] Store holds only `ng_remember`; migration from the old
        full-header slot (delete it, prompt for login)
  - [ ] Delete `PasswordMinter`, `-user`, `set-cookie`; keep
        `NGCHAT_NG_COOKIE`
  - [ ] `ngchat login` / `ngchat logout`; auto-prompt on first run
  - [ ] Signed-out handling: 401 on mint stops the client with a
        distinct error; the TUI exits with `run ngchat login`
  - [ ] Tests: `httptest` fakes for every JSend outcome (success,
        2FA email, 2FA TOTP, bad credentials, lockout, undeliverable,
        401 and 419 on service-token)
  - [ ] Live check with `cmd/smoke` once the site endpoints are on dev

- [ ] **Phase 3 — UI**
  - [ ] Decode `Subscribed.userList`; keep a user list from
        `userJoined`, `userLeft`, `userUpdated`, `away`
  - [ ] Status bar count; `/who`
  - [ ] Mention highlight and bell; `-quiet`
  - [ ] OSC 8 hyperlinks in `internal/render`
  - [ ] Timestamp toggle
  - [ ] `esc` no longer quits; transcript cap at 2000 rows
  - [ ] `away` and `notifications` as faint event rows
  - [ ] `-debug` log with token and cookie redaction; path printed on
        exit
  - [ ] Remove `-channel`; hard-code `general`

- [ ] **Phase 4 — release hygiene and public flip**
  - [ ] `.github/workflows/ci.yml`: vet, test, race on push and PR
  - [ ] Renovate config for Go modules and Actions
  - [ ] `SECURITY.md`
  - [ ] README: login story, `/who`, `-quiet`, `-debug`, timestamps,
        Windows note; drop "Status: scaffold"
  - [ ] `CHANGELOG.md` seeded from `v0.1.0`
  - [ ] End-to-end smoke against dev with the site login live
  - [ ] Repo public; tag `v1.0.0`; confirm artifacts and checksums

## Out of scope for the MVP

- Channel switching (there is one channel)
- Inline re-login inside the TUI
- Images and emote sprites
- Sound playback
- Rich embed layout (`messageEmbeds` renders at most as text)
- Markdown composition (`isMarkdown` on outbound messages)
- Passwordless email-only accounts

## Roadmap after v1.0.0

- **Kitty graphics** for memes and emote sprites. The protocol supports
  frame-based animation (kitty 0.20+), so GIF and APNG play in kitty
  itself; Ghostty and WezTerm draw stills. Needs an emote code→image
  source from the site and a URL fetch path in the render layer.
- **Soundboard**: first the narrated `X played Y` line and a `/sfx`
  list; real audio investigated later with `CGO_ENABLED=0` as a hard
  constraint on the static binary.
- User list as a side pane once the UI gets a real pass.
- Homebrew tap once Releases are steady.
