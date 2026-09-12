# ngchat

Newgrounds Chat in your terminal. A minimalist Go client: one static
binary for macOS, Linux, and Windows.

Chats in `#general`, renders formatted messages, shows who is around,
pings you on mentions, and renews its token in place every hour so an
evening's session never drops. Images (Kitty graphics protocol) and
channel switching are on the [roadmap](docs/roadmap.md); see
[CHANGELOG.md](CHANGELOG.md) for what each release added.

## Install

Grab a binary from [Releases](../../releases), or:

```sh
go install github.com/newgrounds-inc/ngchat-cli/cmd/ngchat@v1.0.0-rc.5
```

Until `v1.0.0` is tagged, `@latest` resolves to the old `v0.1.0`
scaffold rather than the current release candidate, so name the
version.

Each release ships a `checksums.txt` next to the archives. It catches a
corrupt download, not a swapped one: it is published from the same
release as the archives and is not signed. Builds are reproducible
(`-trimpath`, fixed timestamps), so the way to verify an archive is to
build the tagged commit yourself and compare. The Linux and macOS tests
gate every release. Windows builds are best-effort: they
are published and CI runs the tests there, but nobody exercises the TUI
on Windows regularly, so use Windows Terminal and report what breaks.

## Use

```sh
ngchat            # join #general
ngchat -quiet     # same, without the terminal bell on mentions and DMs
ngchat -debug     # also write redacted frames to a log; path printed on exit
ngchat -no-splash # skip the opening animation (NGCHAT_NO_SPLASH=1 does the same)
ngchat -version   # print the version and exit
```

The first run asks for your username or email, password, and a
two-factor code if your account uses one (a TOTP recovery code works at
the same prompt). `ngchat login` does the same on demand. `ngchat
logout` logs this device out on the site, so the stored cookie stops
working everywhere, then clears it locally; if the site cannot be
reached it says so and exits non-zero, because the cookie is still
valid there.

NG Chat is a supporter-only feature. If your account is not one, ngchat
exits with the server's notice after connecting.

Only the site's long-lived remember cookie is stored; it mints
short-lived chat tokens for you. Your password is never written to disk.
If the site refuses the cookie (you changed your password), ngchat exits
and asks you to run `ngchat login` again.

A login is stored per site: pointing `NGCHAT_SITE_URL` at a dev stack
asks for a login there and never sends the production cookie to it.
Both `NGCHAT_SITE_URL` and `NGCHAT_WS_URL` must be `https`/`wss` unless
the host is loopback, and the chat host must be on the site's domain.

### Where the cookie lives

ngchat prefers the OS keyring: macOS Keychain, Windows Credential
Manager, or on Linux a Secret Service provider over D-Bus (GNOME Keyring,
KDE Wallet, or KeePassXC with its Secret Service integration on). When
none answers, it prints `no OS keyring available` and writes the cookie
to `~/.config/ngchat/credentials.json` with mode `0600` instead. That is
fine on a headless box or a server you alone log into; on a shared
desktop, set up a keyring and run `ngchat login` again to move it.

If your account is set to log in with email only, enter your email
address at the first prompt. Mistyped it? Press Enter with no password
to go back.

## Terminal niceties

- `ctrl+s` reveals spoilers, `ctrl+t` toggles timestamps, `pgup/pgdn`
  scrolls, `ctrl+c` quits.
- Typing `@` opens a completion list of the room's users (`tab`/
  `shift+tab` or the up/down arrows move, `enter` accepts, `esc`
  closes; a window under about 9 rows previews the candidate in the
  line instead). `/` as the first character completes slash commands
  you may use (aliases match, the full name is inserted). Starting a
  word with `ng…`/`tf…` completes NG "dank meme" emote shortcodes from
  an embedded list, the same way. Typing `:shortname` completes emoji
  from an embedded catalog, each row showing the glyph next to its
  shortname.
- `/who` lists who is in the channel (mods marked `@`, away users
  dimmed). Every other slash command (`/me`, `/slap`, `/dm`, `/roll`,
  ...) goes to the server, which parses them.
- Mentions and DMs are highlighted and ring the terminal bell; `-quiet`
  keeps the highlight and drops the bell.
- Links are clickable in terminals that support OSC 8 hyperlinks.
- Colors follow the site's `ngchat` theme (orange status bar, soft gold
  names, copper mods, your own name in blue). `NGCHAT_THEME=classic` is
  the legacy gold-on-black. The terminal's own background is kept, so
  a light background will look off.
- The opening animation ends on any key; `-no-splash` or
  `NGCHAT_NO_SPLASH=1` drops it, and a window under about 40×8 never
  shows it.

## Development

Build and test against production, the default endpoints: there is no
public dev stack. Remember that a test run is a real session on the real
site. The site's chat-token limiter counts against your account before
it checks anything else, so a loop that re-mints locks your own browser
out of chat too, and a kick or ban earned while testing is a real one.
`NGCHAT_SITE_URL`, `NGCHAT_WS_URL` and `NGCHAT_ROUTING_COOKIE` exist so
Newgrounds staff can point a build at an internal stack; they do nothing
useful outside the company.

`NGCHAT_NG_COOKIE` (a raw cookie header) bypasses the stored login.
`go run ./cmd/smoke` runs the whole stack headlessly (mint, connect,
authenticate, subscribe, send) and prints event summaries, never tokens;
it is the right tool for protocol or auth changes, since the TUI takes
over the screen.

Design notes live in [CONTEXT.md](CONTEXT.md) and [docs/adr/](docs/adr/).
CI runs vet, the race tests, and a GoReleaser snapshot build on every
pull request and push to `main`; a `v*` tag reruns the Linux and macOS
tests and cuts a release. Security reports:
see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
