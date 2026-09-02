# ngchat

Newgrounds Chat in your terminal. A minimalist Go client: one static
binary for macOS, Linux, and Windows.

**Status: scaffold.** Connects, chats in one channel, renders formatted
messages, renews its token in place every hour. Images (Kitty graphics
protocol), channel switching, and presence are on the roadmap.

## Install

Grab a binary from [Releases](../../releases), or:

```sh
go install github.com/newgrounds-inc/ngchat-cli/cmd/ngchat@latest
```

## Use

```sh
ngchat                    # join #general
ngchat -channel random    # join another channel
```

The first run asks for your username or email, password, and a
two-factor code if your account uses one (a TOTP recovery code works at
the same prompt). `ngchat login` does the same on demand and `ngchat
logout` clears it.

Only the site's long-lived remember cookie is stored, in your OS keyring
(or a `0600` file if no keyring is available); it mints short-lived chat
tokens for you. Your password is never written to disk. If the site
refuses the cookie (you changed your password), ngchat exits and asks you
to run `ngchat login` again.

If your account is set to log in with email only, enter your email
address at the first prompt.

## Terminal niceties

- `ctrl+s` reveals spoilers, `pgup/pgdn` scrolls, `ctrl+c` quits.
- Slash commands (`/me`, `/slap`, `/dm`, `/roll`, ...) work — the server
  parses them.

## Development

Against the NG dev/staging stack:

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_SITE_URL=https://www.newgrounds-d.com
export NGCHAT_ROUTING_COOKIE='serverid=bcolby2'   # dev proxy routing (this project's backend)
```

`NGCHAT_NG_COOKIE` (a raw cookie header) bypasses the stored login.

Design notes live in [CONTEXT.md](CONTEXT.md) and [docs/adr/](docs/adr/).

## License

[MIT](LICENSE)
