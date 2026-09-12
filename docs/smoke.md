# Live verification with `cmd/smoke`

The TUI takes over the screen, so `cmd/smoke` is the tool for verifying
protocol or auth changes; reserve `cmd/ngchat` for UI work. The
harness runs the full stack (mint → connect → authenticate → subscribe
→ send) headless and prints event summaries, never tokens or cookies.

With no variables set it runs against production, which is the only
option outside Newgrounds: the dev stack below is internal to NG staff.

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_SITE_URL=https://www.newgrounds-d.com
export NGCHAT_ROUTING_COOKIE='serverid=<backend>'   # dev proxy routing, every request
export NGCHAT_NG_COOKIE='ng_remember=...'     # raw cookie header; optional
export SMOKE_SECONDS=150                      # optional; default 15
go run ./cmd/smoke
```

The routing cookie is not a secret: it names the dev backend the proxy
should pin you to. `<backend>` is the backend your dev stack runs on;
without it dev answers 503 for every request.

## Verifying the login flow end to end

Without `NGCHAT_NG_COOKIE` the harness uses the remember cookie that
`ngchat login` stored: `NGCHAT_SITE_URL=... ngchat login`, then
`go run ./cmd/smoke`. An exported `NGCHAT_NG_COOKIE` wins over the
stored login, so `unset` it first; the first smoke line names which
source it used. The harness never prompts: with nothing exported and
nothing stored for the site it exits 2 and says to run `ngchat login`,
and a keyring that gave no answer is reported as that rather than as
no login.

## Watching a token renewal

Set `APP_JWT_CHAT_TTL` to 180 seconds on the dev site and run with
`SMOKE_SECONDS=200`: expect a `revalidated` line about every 70s and
no `state=2` (reconnecting) line.

The TTL must stay above the server's two-minute nudge lead: a shorter
one makes the server nudge again the instant each renewal lands, the
client's 5/min renewal budget then refuses the rest, and the token
expires into a reconnect.
