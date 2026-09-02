# ngchat

Newgrounds Chat in your terminal. A minimalist Go client: one static
binary for macOS, Linux, and Windows.

Chats in `#general`, renders formatted messages, shows who is around,
pings you on mentions, and renews its token in place every hour so an
evening's session never drops. Images (Kitty graphics protocol) and
channel switching are on the roadmap; see [CHANGELOG.md](CHANGELOG.md)
for what each release added.

## Install

Grab a binary from [Releases](../../releases), or:

```sh
go install github.com/newgrounds-inc/ngchat-cli/cmd/ngchat@latest
```

Each release ships a `checksums.txt` next to the archives. The Linux
and macOS tests gate every release. Windows builds are best-effort: they
are published and CI runs the tests there, but nobody exercises the TUI
on Windows regularly, so use Windows Terminal and report what breaks.

## Use

```sh
ngchat            # join #general
ngchat -quiet     # same, without the terminal bell on mentions and DMs
ngchat -debug     # also write redacted frames to a log; path printed on exit
ngchat -version   # print the version and exit
```

The first run asks for your username or email, password, and a
two-factor code if your account uses one (a TOTP recovery code works at
the same prompt). `ngchat login` does the same on demand and `ngchat
logout` clears it.

NG Chat is a supporter-only feature. If your account is not one, ngchat
exits with the server's notice after connecting.

Only the site's long-lived remember cookie is stored; it mints
short-lived chat tokens for you. Your password is never written to disk.
If the site refuses the cookie (you changed your password), ngchat exits
and asks you to run `ngchat login` again.

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
- `/who` lists who is in the channel (mods marked `@`, away users
  dimmed). Every other slash command (`/me`, `/slap`, `/dm`, `/roll`,
  ...) goes to the server, which parses them.
- Mentions and DMs are highlighted and ring the terminal bell; `-quiet`
  keeps the highlight and drops the bell.
- Links are clickable in terminals that support OSC 8 hyperlinks.

## Development

Against the NG dev/staging stack:

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_SITE_URL=https://www.newgrounds-d.com
export NGCHAT_ROUTING_COOKIE='serverid=bcolby2'   # dev proxy routing (this project's backend)
```

`NGCHAT_NG_COOKIE` (a raw cookie header) bypasses the stored login.

Design notes live in [CONTEXT.md](CONTEXT.md) and [docs/adr/](docs/adr/).
CI runs vet, the race tests, and a GoReleaser snapshot build on every
pull request and push to `main`; a `v*` tag reruns the Linux and macOS
tests and cuts a release. Security reports:
see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
