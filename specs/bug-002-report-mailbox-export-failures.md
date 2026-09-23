# bug-002 — Report mailbox-level export failures

Status: done
Type: bug

## Outcome

`export_messages` must not present a missing or failed mailbox as a successful
zero-match export. Its compact MCP result must reveal whether the requested
export completed, partly completed, or failed at the mailbox level.

## Constraints

- Add `failed_mailboxes` to the compact result and a `status` of `complete`,
  `partial`, or `failed`. `complete` means no mailbox, message, or attachment
  failures; `failed` means no requested mailbox completed; otherwise any
  failure means `partial`. A successful empty export is `complete`.
- Preserve the manifest path in a `partial` or `failed` result. Keep detailed
  mailbox errors in the manifest, not the compact result. An all-mailboxes-
  failed run returns `status: failed` with a manifest path rather than a
  misleading successful empty summary. Existing top-level connection, login,
  or mailbox-list failures remain tool errors.
- Count mailbox failures in the privacy-safe aggregate failure log alongside
  message and attachment failures. Do not log mailbox names, search terms,
  paths, identifiers, or raw errors.
- If an updated manifest cannot be written after a mailbox failure, return a
  sanitized tool error rather than claiming the manifest records that failure.

## Done when

- [x] Offline fake-server tests cover a missing mailbox, a mailbox selection
      or search failure, and a mixed successful/failed multi-mailbox run.
- [x] Tests distinguish a successful empty export from an all-mailboxes-failed
      export and assert `status`, `failed_mailboxes`, and manifest contents.
- [x] Tests verify message/attachment failures yield `partial` and the
      aggregate failure log includes mailbox failures without sensitive data.
- [x] `go test ./...` passes.
- [x] Targeted live iCloud probe reports a partial result for a missing mailbox
      while the INBOX query matches no messages and creates no message files.
