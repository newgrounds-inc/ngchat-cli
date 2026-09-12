# `internal/splash` — the opening screen

Two seams (ADR 0005):

- `Art`: a monochrome pixel bitmap drawn two pixels per row with
  half-block glyphs. Today the "NG CHAT" wordmark from a 5×7 font,
  scaled by `Fit` to the largest of 1–4 that fits.
- `Effect`: a pure function of elapsed time (`Frame(art, palette, t)`)
  whose timing is fixed so a wider terminal is not a slower splash.

`LaserEtch` is the one effect: a beam sweeps in 1.1 s, pixels cool
hot → warm → ink over 0.45 s, sparks are hashed from the frame index so
frames are deterministic and testable.

A `Palette` of four roles (ink, hot, warm, spark) is mapped from the
theme, so an effect never sees a theme.

How the UI runs the splash (timing, skip keys, small terminals) is in
`ui.md`.
