# spec-007 — Move Messages

Status: done
Type: feature

## Outcome

Move explicitly selected messages from one mailbox to another mailbox.

## Constraints

- Add a `move_messages` tool.
- Accept one to ten message references containing `mailbox`, `uid_validity`,
  and `uid`, plus one required destination mailbox.
- Require all message references in a request to use the same source mailbox.
- Require the destination to be a different mailbox name returned by
  `list_mailboxes`; do not create or rename mailboxes.
- Use UID-based move operations and never sequence numbers.
- Do not infer moves from search results, flags, or message content.
- Return one result per requested message with its source reference, destination
  mailbox, status, and a sanitized error when applicable.
- Return destination UID information when the server provides it; do not claim
  a destination reference when it is unavailable.
- Never permanently expunge messages as part of a move. Moving to
  `Deleted Messages` is allowed, but permanent deletion is outside this spec.
- One failed message must not hide successful moves for other messages.
- Do not log credentials, message content, or live IMAP transcripts.

## Done when

- [x] `move_messages` exposes the documented typed input and output.
- [x] Tests reject empty or mixed source mailboxes, same-source destinations,
      unknown destinations, stale UIDVALIDITY, and invalid UIDs.
- [x] Fake-server tests cover successful moves and per-message failures.
- [x] Tests verify the operation uses UIDs and does not permanently expunge
      messages.
- [x] `go test ./...` passes.
