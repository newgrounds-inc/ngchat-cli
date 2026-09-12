# AGENTS.md

Guidance for coding agents working in this repository. `CLAUDE.md`
imports it so Claude Code picks it up too. Read the linked docs when
their trigger applies; do not read them all up front.

## Commands

```sh
go build ./cmd/ngchat            # build the binary
go vet ./...                     # vet everything
go test ./...                    # unit tests (no network, no keyring)
go test -run TestDecodeTolerance ./internal/protocol   # a single test
go test -cover ./internal/...    # coverage per package
goreleaser release --snapshot --clean     # local cross-platform build check
NGCHAT_UPSTREAM=~/dev/ngchat go generate ./internal/complete   # regen embedded lists
```

Verifying a protocol or auth change live, watching a token renewal, or
running against the NG-internal dev stack: `docs/smoke.md` (the
headless `cmd/smoke` harness; `cmd/ngchat` is for UI work only).

## Architecture

Four layers, each one package, with a channel as the only seam between
network and UI, plus three pure packages the UI draws with. Each has a
doc in `docs/internals/` naming its invariants and gotchas; read it
before changing that package.

- `internal/auth` — credentials, site login, chat JWT minting, the
  keyring/file store. `docs/internals/auth.md`, with
  `docs/site-login-endpoints.md` as the site contract.
- `internal/protocol` — hand-ported wire types: strict out, tolerant
  in. Syncing against upstream, and the generated emote/emoji lists:
  `docs/internals/protocol.md`.
- `internal/client` — the session state machine: renewal in place,
  gap detection, stop vs retry. `docs/internals/client.md`.
- `internal/render` — server HTML to ANSI, and the control-character
  boundary every network string crosses. `docs/internals/render.md`.
- `internal/theme` — colors by role, never by value.
  `docs/internals/theme.md`.
- `internal/splash` — the opening screen's art and effect.
  `docs/internals/splash.md`.
- `internal/complete` — tab-completion sources and ranking.
  `docs/internals/complete.md`.
- `internal/ui` — Bubble Tea: transcript anchoring, splash timing,
  the completion list. `docs/internals/ui.md`.

The protocol, the slash-command table, the emote list and the emoji
catalog all drift from the private `ngchat` repo by hand or by
`go generate` (ADR 0002); a change upstream is a change here.

## Conventions

- Every exported type, function, and package carries a doc comment
  explaining *why*, not what. Match that density; comments here earn
  their place by naming a constraint or a gotcha.
- Decisions with tradeoffs go in `docs/adr/` as short prose ADRs
  (context → options → consequences). Domain vocabulary lives in
  `CONTEXT.md`; use those terms ("re-mint", "backfill buffer", "gap",
  not "refresh", "scrollback", "history").
- `docs/roadmap.md` lists what is out of scope and planned;
  `docs/manual-test-plan.md` is the hands-on pass per platform.
- Secrets never touch stdout or logs; `cmd/smoke` prints summaries
  only.
- Tests are table-driven and hermetic: no network (use `httptest`), and
  no writes to the real OS keyring or config dir (override
  `XDG_CONFIG_HOME` and exercise the file fallback). Terminal styling
  is stripped with an ANSI regexp before comparing, so assertions hold
  under any color profile.
