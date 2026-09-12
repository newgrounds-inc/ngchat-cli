# `internal/protocol` — hand-ported wire types

The source of truth is the TypeScript Zod schemas in the private
`ngchat` repo (`src/shared/protocol/`), with no compile-time link.
Protocol changes upstream require a manual sync here (ADR 0002).

## Strict out, tolerant in

The asymmetry matters:

- `client.go` structs are **strict**: the server rejects unknown
  fields, so they must match field-for-field.
- `Decode` in `server.go` is **tolerant**: unknown names and
  undecodable payloads become `Unknown`, never an error, so old
  binaries survive protocol additions.

## The same drift contract in `internal/complete`

Three embedded lists are reconciled against upstream the same way:

- The slash-command table in `internal/complete/commands.go` is
  reconciled by hand against upstream `slash_commands.ts`; its comment
  names the upstream commit.
- The emote shortcode list (`emotes_gen.go`) and the emoji catalog
  (`emoji_gen.go`) are generated: `NGCHAT_UPSTREAM=<checkout> go
  generate ./internal/complete` (no default for the variable) reads
  upstream's `emoticons.json`/`emoticons-small.json` and
  `emoji_catalog.generated.ts` and writes both files' headers naming
  the upstream commit. Regenerate whenever upstream's emoticon lists or
  emoji catalog change.
