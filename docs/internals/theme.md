# `internal/theme` — colors by role

A `Theme` carries the DaisyUI roles the web client's `styles.css`
declares (base, primary, secondary, accent, neutral, info, success,
warning, error) plus the chat-only `Username`; `ngchat` and `classic`
are sRGB copies of the site's two blocks. `NGCHAT_THEME` picks a theme
by name. See ADR 0005.

The UI builds every style once from a theme (`newStyles`) and names
roles, never colors, so a theme is a copy of numbers.

Colors are exact; Bubble Tea's renderer downsamples them to the
terminal and drops them under `NO_COLOR`. That is why the status bar
and mention highlight also carry Reverse: the attribute survives where
the color does not. The terminal's background is never painted.
