# ngchat

Newgrounds Chat in your terminal. A minimalist Go client: one static
binary for macOS, Linux, and Windows.

**Status: scaffold.** Connects, chats in one channel, renders formatted
messages, survives the hourly token bounce. Images (Kitty graphics
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

Login today (while the site's CLI login endpoints are in the works):

- **Allowlisted bot accounts** get a username/password prompt.
- **Everyone else**: log into newgrounds.com in a browser, then run
  `ngchat set-cookie` and paste your cookie header. It's stored in your
  OS keyring (or a `0600` file if no keyring is available) and used to
  mint short-lived chat tokens. `ngchat logout` clears it.

Your NG password is never written to disk.

## Terminal niceties

- `ctrl+s` reveals spoilers, `pgup/pgdn` scrolls, `ctrl+c` quits.
- Slash commands (`/me`, `/slap`, `/dm`, `/roll`, ...) work — the server
  parses them.

## Development

Against the NG dev/staging stack:

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_JWT_URL=https://www.newgrounds-d.com/ngapps/jwt.php
export NGCHAT_ROUTING_COOKIE='serverid=...'   # dev proxy routing
```

Design notes live in [CONTEXT.md](CONTEXT.md) and [docs/adr/](docs/adr/).

## License

[MIT](LICENSE)
