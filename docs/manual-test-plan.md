# Manual test plan for the public launch

Status: draft 2026-09-02
Owner: Brendon C.
Related: `docs/roadmap.md`, ADR 0001, ADR 0003

Automated tests cover the protocol, the login flow against fakes, and
the rendering. What they cannot cover is a real terminal on a real OS
with a real keyring, so this is the hands-on pass before the repo goes
public. Run the whole of section 1 on every platform; the rest is
"once anywhere" unless a row says otherwise.

Platforms in hand: Linux/amd64, macOS/arm64, Windows/amd64 (VM),
Windows/arm64 (VM). Tick a box per platform where it says (each).

Use the dev stack throughout so bans and kicks are harmless (it is
internal to NG staff; outside the company this plan runs against
production, where they are not):

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_SITE_URL=https://www.newgrounds-d.com
export NGCHAT_ROUTING_COOKIE='serverid=<backend>'
```

`<backend>` is the dev backend your stack runs on; see `docs/smoke.md`.

On Windows (PowerShell) that is `$env:NGCHAT_WS_URL = '...'` and so on.

Run with `-debug` for anything involving renewal, reconnects, or
refusals; the log is the evidence when something looks wrong. It
redacts tokens and cookies but keeps chat text, so do not attach one
to a public issue without reading it.

## 1. Install and first run (each platform)

- [ ] Download the platform archive from the CI snapshot artifact (or a
      draft release), verify it against `checksums.txt`
      (`sha256sum -c`, `shasum -a 256 -c` on macOS, `Get-FileHash` on
      Windows), and extract it. The archive holds the binary, LICENSE,
      README.md and CHANGELOG.md.
- [ ] `ngchat -version` prints the version, not `dev`.
- [ ] macOS only: extract with Finder (Archive Utility), not `tar`, and
      run the binary. Gatekeeper may refuse an unsigned, quarantined
      binary; note the exact wording. If it refuses, the README needs
      a `xattr -d com.apple.quarantine ngchat` line and signing goes on
      the roadmap.
- [ ] `ngchat` with nothing stored prompts for identity, then password
      (hidden), then a code. After login the TUI opens on `#general`.
- [ ] Keyring took the credential: macOS Keychain Access shows an
      `ngchat` item; Windows Credential Manager shows one under Windows
      Credentials; Linux with a Secret Service shows it in Seahorse or
      KeePassXC. On a headless Linux box you get `no OS keyring
      available` once and `~/.config/ngchat/credentials.json` with mode
      `0600` instead.
- [ ] Second run: no prompt, straight into the channel.
- [ ] `ngchat logout` prints `credentials cleared`, the keyring entry
      is gone, and the next run prompts again.
- [ ] `ctrl+c` in the TUI exits, the shell prompt comes back with the
      screen restored (no alt-screen residue, cursor visible, colors
      reset, keyboard echo working). Type a command to confirm.

## 2. Login edge cases (once, any platform)

- [ ] Email address at the identity prompt works the same as a
      username.
- [ ] Wrong password re-asks the password with the site's message; the
      identity is not asked again.
- [ ] Empty password returns to the identity prompt with the previous
      entry as the default (Enter keeps it). No request is sent (check
      the site's login limiter does not tick).
- [ ] Account without an authenticator: the code arrives by email and
      is accepted.
- [ ] Account with TOTP: the six-character code is accepted; a recovery
      code (longer) at the same prompt is accepted and consumed.
- [ ] Three wrong codes restart from the password step.
- [ ] Let an emailed code go stale (or use one from an earlier
      attempt): the flow restarts from the password step, not a crash.
- [ ] Lockout after repeated wrong passwords: ngchat exits with the
      site's message and a non-zero status.
- [ ] `ctrl+c` at any prompt exits quietly with status 130 and no error
      text.
- [ ] Log in, then change the password on the site (or log out every
      device): the next run says signed out and prints `run ngchat
      login`; no reconnect loop, no repeated mint attempts (the
      service-token limiter is 10/min and a loop would lock the
      browser out too).
- [ ] Upgrade path: a v0.1.0 install with a stored cookie header. The
      new binary prints that the old cookie was removed and prompts for
      login once.

## 3. Entry gate and moderation (once, needs a second account and a mod)

Each refusal must show the server's notice on exit, never a blank
screen or a reconnect loop.

- [ ] Non-supporter account: notice on exit.
- [ ] Unvalidated e-mail: notice on exit.
- [ ] Under-18 account: notice on exit.
- [ ] Site-banned account: notice on exit.
- [ ] Channel-banned account (and an alt of one, if the server has the
      rule): notice on exit, no reconnect.
- [ ] Kicked while connected: transcript shows `disconnected:` with the
      mod's reason, TUI stops, exit is clean.
- [ ] Ban while connected, then reconnect: refused with the notice.
- [ ] Idle timeout: stay silent for the server's idle window; the stop
      reason is `idle timeout` and there is no reconnect.
- [ ] After the hourly renewal (section 6) revalidates with a changed
      rank, the `/` command list follows: a promotion shows the
      newly-available commands, a demotion hides them, with no
      reconnect needed.
- [ ] `/k` as a mod shows `/kick (k)`.

## 4. Chat basics (each platform; this is the terminal-dependent part)

- [ ] Send a message; it appears with your name in the self color; the
      other client sees it.
- [ ] Receive bold, italic, a link, an emote (`:code:` text), a
      spoiler (hidden bar; `ctrl+s` reveals and hides all of them).
- [ ] A very long message wraps inside the viewport; no horizontal
      scroll, no status bar pushed off the bottom.
- [ ] Emoji and CJK in a message: widths correct, no misaligned rows.
- [ ] Resize the terminal narrower and wider: the transcript reflows,
      the status bar stays one line, the input stays at the bottom.
- [ ] Links are clickable (OSC 8) in a terminal that supports it, and
      the visible URL is still readable in one that does not.
- [ ] `/me`, `/slap`, `/roll` render as `* name ...` event rows; a
      server message renders faint.
- [ ] A DM to you shows the `[DM]` prefix and rings the bell.
- [ ] Typing indicator: the other client typing shows `name typing…`
      in the status bar and clears.
- [ ] `pgup`/`pgdn` scroll the transcript; new messages while scrolled
      up do not yank the view.
- [ ] Scrolled up, the help row reads `↓ more messages below · end to
      jump`; `end` returns to the bottom and the hints come back.
      Typing a character, or sending, also returns to the bottom.
- [ ] Scrolled up next to a spoiler, `ctrl+s` and `ctrl+t` keep the
      same row at the top of the screen (rows above grow or shrink; the
      view does not jump to the bottom).
- [ ] `ctrl+t` shows `HH:MM` local-time prefixes; toggles off again.
- [ ] `esc` does nothing (it used to quit).
- [ ] MOTD renders once below the backfill on join.
- [ ] `/` alone lists every command you may use, alphabetical, each
      with its description dimmed.
- [ ] `/k` as a regular user shows nothing.
- [ ] Accepting a candidate matched by an alias (e.g. `airhorn` for
      `/ah`) inserts the full canonical name, never the alias.
- [ ] `hi /k` (a `/` mid-line, not the first character) opens nothing.
- [ ] Move the cursor into the middle of a typed name (e.g. `/ki|ck`,
      cursor after "ki") and accept: only the span up to the cursor is
      replaced, and the "ck" typed after the cursor stays in place
      after the inserted text.
- [ ] Typing `ngaho` opens the emote list with `ngaHoldup` first;
      `enter` accepts it, leaving `ngaHoldup ` in the line with no
      other delimiter.
- [ ] An ordinary word like `ngl` opens nothing.
- [ ] `esc` on a word that opened the emote list keeps it quiet while
      you keep typing the same word, and it opens again on the next
      new word.
- [ ] Typing `:smi` opens the emoji list with `:smile:` first; `enter`
      accepts it, leaving `:smile: ` in the line.
- [ ] The glyph column lines up for emoji-presentation glyphs: eyeball
      `:woman_lifting_weights_tone1:` (a skin-tone glyph) and
      `:detective:` (one of a handful the terminal may render
      narrower than the column expects) against an ordinary row like
      `:smile:`.
- [ ] `:)`, `:D` and `:-)` open nothing.

- [ ] The splash: the wordmark sweeps in over about 1.5 s, holds until
      online, then the chat screen replaces it with no residue. Any key
      skips it. `ngchat -no-splash` and `NGCHAT_NO_SPLASH=1` go straight
      to chat. A window narrower than 40 columns shows no splash.
- [ ] Colors match the site: orange status bar with dark text, soft
      gold names, copper `@mods`, your own name blue, gold-on-dark
      mention highlight, red `[DM]`. `NGCHAT_THEME=classic` turns names
      NG gold and the bar the legacy orange; `NGCHAT_THEME=nope` exits
      at once naming the valid themes.

## 5. Roster and mentions (once, two clients)

- [ ] Status bar count matches the site's user list.
- [ ] `/who` lists names, mods marked `@`, away users dimmed with their
      away message. It is handled locally and never sent.
- [ ] Join and part from the other client update the count and add
      faint event rows.
- [ ] Set away on the other client: the roster and `/who` reflect it;
      an away row appears.
- [ ] A mention (`@you`) is highlighted and rings the bell; `-quiet`
      keeps the highlight and drops the bell.
- [ ] Reconnect (section 6) with a mention in the backfill: highlighted
      but silent.
- [ ] The notifications inbox replays on the first subscribe only
      (newest 10, faint); a reconnect does not replay it.
- [ ] `@` alone opens a list of everyone in the room but you,
      alphabetical, away users dimmed.
- [ ] `@al` narrows the list and `tab` cycles the highlight.
- [ ] `enter` inserts `@name ` and sends nothing until `enter` is
      pressed again.
- [ ] `esc` closes the list, and typing more into the same word does
      not reopen it.
- [ ] The other client joining or leaving shows up in the next list you
      open, with no restart needed.
- [ ] A 40×8 window previews the highlighted candidate in the input
      line instead of drawing a list.

## 6. Renewal and reconnect (once, `-debug` on)

- [ ] Renewal: with the dev site's `APP_JWT_CHAT_TTL` at 180 seconds,
      sit for 5 minutes. The debug log shows `revalidate` →
      `reauthenticate` → `revalidated` about every 70s, no
      `reconnecting`, no gap row, and the status stays `online`.
- [ ] Renewal at the real TTL: sit for 65 minutes on the normal dev
      TTL. Same expectation, once.
- [ ] Drop the network for 10 seconds (Wi-Fi off, VM adapter
      disconnected): status shows `reconnecting… (reason)`, backoff
      doubles in the log up to 30s, then `online` again; messages sent
      by the other client during the drop appear from the backfill,
      no gap row.
- [ ] Drop the network for long enough that more than 25 messages are
      sent meanwhile: the gap row `— reconnected, older messages
      missing —` appears exactly once.
- [ ] Laptop sleep and wake (lid closed 5 minutes): reconnects on its
      own, no stuck `connecting…`.
- [ ] Kill the dev chat server (or the WebSocket via the proxy): the
      client backs off and recovers when it returns.

## 7. Exit hygiene and the debug log (each platform)

- [ ] `-debug` prints `debug log written to <path>` as the last line on
      exit, on every exit path: `ctrl+c`, kicked, signed out, denied.
- [ ] The path is right: `~/.local/state/ngchat/debug.log` on Linux
      (`$XDG_STATE_HOME` if set), `~/Library/Logs/ngchat/debug.log` on
      macOS, `%AppData%\ngchat\debug.log` on Windows. Mode `0600` on
      Linux and macOS.
- [ ] Grep the log for `token`, `Bearer`, `ng_remember`, `XSRF`,
      `newgrounds_session`: every value is redacted. Heartbeats are
      absent.
- [ ] Exit status: 0 on `ctrl+c`, non-zero on denied and signed out.
- [ ] The terminal bell actually sounds (or flashes, per terminal
      setting) on this platform.

## 8. Terminal matrix

One row per terminal you have. Note bell, OSC 8, colors, and `ctrl+s`
(some terminals eat it as XOFF flow control: if the screen freezes,
`ctrl+q` resumes and the README needs a note for that terminal).

- [ ] Linux: kitty, Ghostty, GNOME Terminal, inside tmux (OSC 8 needs
      `allow-passthrough` or tmux 3.4+; bell depends on
      `bell-action`).
- [ ] macOS: Terminal.app (no OSC 8, bell as flash by default), iTerm2,
      Ghostty.
- [ ] Windows: Windows Terminal with PowerShell and with cmd.exe;
      legacy conhost (expect degraded colors, note what breaks); inside
      WSL as a Linux binary for comparison.
- [ ] `NO_COLOR=1`: still readable, no raw escape codes on screen; the
      status bar and mention highlight are still reverse-video bars,
      and the splash still sweeps in monochrome.
- [ ] `TERM=dumb ngchat`: plain text throughout (no bars, no bold), no
      raw escape codes on screen, splash still legible.
- [ ] A 256-color terminal (`TERM=xterm-256color` without
      `COLORTERM`): the theme downsamples to nearby colors, nothing
      turns default-white.
- [ ] Emoji completion's glyph column (type `:smi`): glyphs render and
      the shortname column lines up for ordinary rows on each terminal
      above; also eyeball `:woman_lifting_weights_tone1:` and
      `:detective:` and note which terminal, if any, renders either
      narrower than the column expects.

## 9. Windows specifics (both VMs)

- [ ] arm64 build runs natively on the arm64 VM (not under emulation);
      amd64 build on the amd64 VM.
- [ ] Windows Defender SmartScreen or antivirus does not quarantine the
      binary; if it does, note the wording for the README.
- [ ] Credential Manager entry appears and `logout` removes it.
- [ ] `ctrl+c` exits cleanly; the console is usable afterwards.
- [ ] Window resize and maximize reflow correctly.
- [ ] Unicode and emoji width (Windows Terminal handles it; conhost
      may not).

## 10. Release rehearsal (once, after the repo is public)

- [ ] CI is green on `main` for all three OSes (Windows may be yellow),
      and the release workflow's Linux and macOS test job passed before
      GoReleaser ran.
- [ ] Renovate opened its onboarding or first PRs and the config was
      recognized (no "onboarding" PR asking to create the config).
- [ ] Private vulnerability reporting is enabled under Settings →
      Security; the "Report a vulnerability" button that SECURITY.md
      points to exists.
- [ ] `go install github.com/newgrounds-inc/ngchat-cli/cmd/ngchat@latest`
      works from a clean machine and `ngchat -version` reports the tag.
- [ ] Tag `v1.0.0`: the release has six archives plus `checksums.txt`,
      the checksums verify, the notes are grouped into Features and Bug
      fixes with docs and chores filtered out, and the footer links to
      CHANGELOG.md.
- [ ] `CHANGELOG.md` has `[1.0.0] - date` in place of `[Unreleased]`
      before the tag is pushed.
- [ ] The production endpoints work with no env vars set: unset the
      three dev variables and log in against www.newgrounds.com.
