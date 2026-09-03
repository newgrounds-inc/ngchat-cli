# One stored credential per site origin, TLS everywhere but loopback

Amends ADR 0003. The remember cookie is a 400-day bearer for the site
that issued it, but v1.0.0-rc.1 kept it under one global key and seeded
it into whatever `NGCHAT_SITE_URL` named. Switching to the dev stack
with a production login stored sent the production cookie to dev
(which then answered 401 and prompted a login that overwrote the
production one), and a typo or a plaintext URL would have sent it
anywhere.

The store now keys the credential by site host: the bare `ng_remember`
slot for `www.newgrounds.com`, so every existing login keeps working,
and `ng_remember@<host>` for any other origin. `NewSite` refuses a
non-`https` origin and the client refuses a non-`wss` chat URL unless
the host is loopback, where plaintext is the local dev stack talking to
itself. The jar marks the cookie `Secure` on an `https` site so a
redirect to plaintext cannot carry it.

## Considered Options

- **Named profiles (`prod`, `dev`) instead of free URLs** — rejected for
  now. The URL overrides are how every dev stack (`newgrounds-d.com`,
  per-developer hosts) is reached; a fixed list would need editing for
  each one. Keying by host gives the same isolation without the list.
- **Refuse to load any credential when the URL is overridden** — rejected:
  it makes the dev stack unusable without `NGCHAT_NG_COOKIE`, which is
  the pasted-header path ADR 0003 retired for real use.
- **Leave the site/WS pair unvalidated** — rejected after review. The
  chat JWT minted from the site's cookie is the first frame sent to the
  WS URL, so a wrong one has the token before `authenticate` can fail.
  `Site.CheckChatURL` requires the chat host to share the site's
  registrable domain (loopback with loopback, an IP literal with
  itself), which every real pairing satisfies: `www`/`chat` on
  `newgrounds.com`, `newgrounds-d.com`, or a local stack.
- **Follow HTTP redirects** — rejected. The API never redirects, cookie
  scope ignores the port, and a 307 re-sends the login body, so a
  redirect from the site is returned as the failure it is rather than
  followed.

## Consequences

- One login per site. `ngchat logout` acts on the site the run is
  pointed at and leaves other sites' credentials alone.
- A `NGCHAT_SITE_URL` of `http://...` off loopback is an error at
  startup, not a request; same for `ws://`, and for a chat host outside
  the site's domain.
- `cmd/smoke` follows the same key, so a stored dev login is what it
  uses against dev.
