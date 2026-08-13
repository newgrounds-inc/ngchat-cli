# Go with hand-ported protocol types, tolerant of server drift

The protocol's source of truth is the TypeScript Zod schemas in the
private `ngchat` repo (`src/shared/protocol/`). We chose Go — for tiny
static cross-platform binaries and Bubble Tea — over compiling the
TypeScript client (which would reuse the schemas verbatim but ships
50-90 MB binaries with a weak TUI ecosystem). The cost is hand-ported
types with no compile-time link to the schemas, and there is no protocol
versioning on the wire.

## Consequences

- Client→server messages are strict-validated by the server: our send
  structs must match exactly, field for field.
- Server→client decoding must stay tolerant: unknown message names and
  undecodable payloads become an ignorable `Unknown`, never an error, so
  old binaries survive protocol additions.
- Protocol changes in `ngchat` require a manual sync here; `ngchat`'s
  AGENTS.md carries a pointer reminding maintainers.
