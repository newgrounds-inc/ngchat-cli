# Theming and opening splash

Status: agreed 2026-09-03
Owner: Brendon C.
Related: ADR 0005, `newgrounds-inc/ngchat` `src/client/app/styles.css`
(the theme source), omarchy.org's mark (the model for the splash)

## Where things stood on 2026-09-03

The TUI drew with ANSI palette slots, so colors were the terminal's,
not the site's. The web client had two DaisyUI themes (`ngchat`,
`classic`) declared as semantic roles. There was no opening screen; the
first frame was a bare "connecting…" until the first window size.

## Decisions

- **Roles, not colors.** `internal/theme.Theme` carries DaisyUI's roles
  plus `Username`; the UI builds every style from it once. A theme is a
  copy of the site's CSS block (oklch converted to sRGB hex).
- **Two seams for the splash.** `splash.Art` (a pixel bitmap, rendered
  with half-block glyphs so pixels are square) and `splash.Effect`
  (`Frame(art, palette, t)`, pure in `t`). The palette is four roles
  (ink, hot, warm, spark) mapped from the theme, so an effect is
  written once and every theme lights it.
- **Wordmark only for now.** A pixel tank from the PWA icon was weighed
  and deferred: it needs truecolor and a fallback path, and the
  wordmark alone carries the brand.
- **Timing is fixed, not per column**, so a wider terminal (a larger
  scale) is not a slower splash. About 1.5 s; holds for the client to
  come online, at most 4 s; any key skips.
- **The terminal keeps its background.** See ADR 0005.
- **Environment, not flags, for choices that persist** (`NGCHAT_THEME`);
  a flag for a per-run switch (`-no-splash`, mirrored by
  `NGCHAT_NO_SPLASH` for scripts).

## Progress

- [x] `internal/theme` with `ngchat` and `classic`, tests that every
      role is set.
- [x] UI styles derived from the theme; status bar and mention keep
      reverse video for colorless terminals.
- [x] `internal/splash`: wordmark bitmap, half-block renderer, laser
      etch, deterministic frame tests.
- [x] Splash phase in the UI: fit to terminal, skip on key, hold for
      online, cap at 4 s, quit-on-stop still wins.
- [x] `NGCHAT_THEME`, `-no-splash`, `NGCHAT_NO_SPLASH`, usage text.
- [ ] Hand-test on the terminal matrix (`docs/manual-test-plan.md`
      §4 and §8): colors under kitty/Ghostty/Terminal.app/Windows
      Terminal, `NO_COLOR`, a 40-column window, tmux.

## Later, when wanted

- **More themes.** Copy the site's block. If the site grows a theme
  picker with persisted choice, this client could read the same name.
- **More effects.** Candidates that suit a wordmark: scramble-decode
  (glyphs settle from noise), pixel rain (columns fall into place),
  a Terminal Text Effects-style "beams". Pick with `NGCHAT_SPLASH=<name>`
  once there are two; `random` once there are three.
- **More art.** The tank on its sunburst as a second `Art`, rendered
  from the PWA icon with two colors per cell (foreground and
  background on `▀`), shown above the wordmark when the terminal has
  truecolor and room. A `Fit` that composes two arts.
- **A config file** (`$XDG_CONFIG_HOME/ngchat/config.toml`) once a
  third persistent setting exists; until then the environment is the
  config.
- **A light theme** would need the base-content roles revisited, since
  the terminal's background is not painted.
