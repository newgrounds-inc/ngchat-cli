# Theme roles from the site, and an opening splash

The TUI colored names and highlights with ANSI palette slots (10, 11,
14, 13, 9), so what a user saw depended on their terminal's palette and
never matched the web client. The web client meanwhile has two DaisyUI
themes (`ngchat`, the site's warm orange on near-black, and `classic`,
the legacy gold-on-black), each declared as semantic roles: base,
primary, secondary, accent, neutral, info, success, warning, error,
plus a chat-only `username`. The first request that touched this was an
opening animation in the site's colors, which had nowhere to get them.

`internal/theme` now carries those roles, with both site themes ported
as sRGB copies of the CSS values. Every style the UI draws with is
built once from a `Theme` (`newStyles`), so a widget names a role and
never a color, and `NGCHAT_THEME` picks the theme. `internal/splash`
holds the opening screen as two seams: an `Art` (a pixel bitmap drawn
with half-block glyphs, today the "NG CHAT" wordmark) and an `Effect`
(a pure function of elapsed time, today a laser-etch sweep). The UI
plays the effect until it finishes and the client is online, or for at
most four seconds, and any key skips it.

## Considered Options

- **Keep terminal palette slots** — rejected. They are the user's
  colors, not the site's; the same transcript reads warm in one
  terminal and neon in the next, and no splash can be drawn in a
  brand color that is not a role.
- **Per-widget colors instead of roles** ("userColor", "modColor")
  — rejected. A second theme would then be a second copy of every
  widget's choices. Roles are what the site's CSS already declares, so
  a port is a copy of numbers, and a role's meaning ("accent is what
  must stand out") is decided once.
- **Paint the whole screen in the theme's base color** — rejected for
  now. Omarchy does it for its screensaver, but the chat screen after
  the splash would then either switch backgrounds (jarring) or paint
  every cell every frame (a cost with no benefit on a dark terminal,
  and a fight with a light one). The terminal keeps its background;
  text roles were tuned against the site's dark base, so a light
  terminal is a known degradation.
- **Reverse video for the status bar and mention highlight, with
  colors on top** — chosen. Under `NO_COLOR` the renderer drops
  colors but keeps attributes, so both still read as a bar there.
  `TERM=dumb` is the no-TTY profile and drops attributes too, leaving
  plain text; a bar there would need text delimiters, which nothing
  asks for. Foreground and background are declared swapped because
  reverse swaps them back.
- **A Go port of Terminal Text Effects** — rejected as a dependency
  that does not exist; one effect behind an interface is ~150 lines.
- **A config file for theme and effect** — deferred. Two environment
  variables cover today's choices; a file earns its place when a
  third setting shows up (see `docs/plans/theming-and-splash.md`).
- **Splash before the first frame, blocking the network** — rejected.
  The client goroutine connects underneath, the splash covers the
  first moments, and the chat screen is laid out throughout, so the
  handover is a redraw rather than a wait.

## Consequences

- The transcript looks the same in every terminal with color, and
  matches the site: soft gold names, copper mods, orange status bar,
  gold mention highlight, red `[DM]`, and self in the theme's one
  cool color (info) so your own lines are found at a glance.
- Adding a theme is a copy of the site's CSS block into
  `internal/theme`. Adding an effect is a type with `Frame`; adding
  art is a bitmap. None of them touch the UI.
- `-no-splash` / `NGCHAT_NO_SPLASH` exist from the start for scripts,
  screen readers, and anyone who has seen it enough. A terminal too
  small for the wordmark at scale 1 (under about 40×8) plays none.
- A stop that ends the run (signed out, access denied) still quits
  during the splash, so the one useful line prints at once.
