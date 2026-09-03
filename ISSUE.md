# Bound or chunk oversized `subscribed` WebSocket payloads

## Summary

The server sends the initial channel state as one `subscribed` WebSocket
frame. That frame combines the backfill, notifications, the current user's
away message, and the complete channel roster. The roster and its rendered
away-message fields do not have an enforced byte limit.

Legal message text can expand substantially when `formatText` turns Unicode
emoji into HTML. As a result, the server can produce a schema-valid frame
larger than a client's bounded WebSocket read limit. Increasing or removing
the client limit is not a durable fix: it only moves the failure threshold or
allows an oversized frame to exhaust client memory.

This is an availability issue, not a code-execution or credential-disclosure
issue. An affected client cannot finish subscribing to the channel and may
enter a reconnect loop.

## Verified evidence

Tested against `ngchat@b5253612fbd48b3a0947584b996cfd9dfd626276`:

- Formatting a legal 5,000-character string of U+274E produced 495,000 bytes
  of rendered HTML plus 15,000 bytes of raw UTF-8 text. Unicode emoji
  expansion is not counted by the custom-emoticon limit.
- A Node-style JSON `subscribed` envelope containing the documented 25
  backfill messages, 100 notifications, the top-level away-message pair, and
  one roster row measured 67,325,852 bytes. That is 216,988 bytes above the
  ngchat CLI's 64 MiB (67,108,864-byte) defensive read limit.
- The Go protocol decoder accepted that envelope as a valid `Subscribed`
  message.
- The current server path fetches 25 notifications rather than 100. With that
  current behavior, the same formatter-aware calculation crosses 64 MiB at
  373 full-size roster away-message rows.

Relevant server paths include:

- `src/server/server.ts` (`getUserList` and subscription assembly)
- `src/server/format_text.ts`
- `src/server/formatters/markup-processor.ts`
- `src/server/slash-commands/away.ts`
- `src/server/db/queries.ts` (`getNotifications`)

## Expected behavior

The protocol should define an enforceable maximum size for every outbound
WebSocket frame. A valid channel state must always fit within that bound,
regardless of roster size or worst-case formatter expansion.

## Proposed solution

Prefer chunking the roster instead of placing it entirely in `subscribed`:

1. Keep `subscribed` limited to bounded channel metadata, backfill, and
   notification data.
2. Send the roster in byte-bounded chunks using explicit continuation and
   completion semantics.
3. Cap or truncate away-message previews included in roster rows. Apply the
   budget to the rendered output as well as the raw input because formatting
   can expand the text.
4. Enforce a byte budget while constructing outbound messages, then assert
   the final serialized frame size before sending it.
5. Document the limits in the shared protocol and update clients to derive
   their read limits from that contract.

A smaller short-term fix would cap/truncate roster away previews and cap the
number of roster rows in `subscribed`, but it must define how clients obtain
the omitted roster data. Merely raising the client's 64 MiB limit does not
resolve the unbounded protocol contract.

## Acceptance criteria

- Every server-produced WebSocket frame has a documented, enforced byte
  ceiling after formatting and JSON serialization.
- A large roster is delivered in bounded chunks or through another explicitly
  bounded mechanism; users are not silently omitted.
- Away-message previews have enforced raw and rendered-output limits.
- Tests cover worst-case formatter expansion, the maximum backfill and
  notification counts, and a roster large enough to require chunking.
- An oversized logical subscription cannot disconnect a bounded client or
  cause an automatic reconnect loop.
- Protocol documentation and the browser and CLI clients are updated for any
  new chunking events or limits.
