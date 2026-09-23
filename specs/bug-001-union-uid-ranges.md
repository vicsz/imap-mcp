# bug-001 — Union UID ranges in search and export

Status: done
Type: bug

## Outcome

`search_mail` and `export_messages` return messages in any requested UID range,
including when the ranges are disjoint. Multiple ranges must not silently
produce an empty result because IMAP intersects separate search keys.

## Constraints

- Treat each `{start, end}` range as inclusive. Union all `uid_ranges` into one
  typed IMAP UID set and use it as one search criterion; AND that criterion
  with the other supplied filters.
- Keep current range validation, mailbox selection, result ordering, search
  limit, and export behavior. Overlapping ranges must not duplicate messages.
- Do not accept raw IMAP syntax, use sequence numbers, or expose live UIDs in
  test output or the repository.

## Done when

- [x] Offline fake-server tests for both tools select the expected message
      identities from two disjoint singleton ranges and two nonadjacent spans.
- [x] Tests cover an absent UID, overlapping ranges without duplicates, and a
      UID-range union combined with another filter.
- [x] A genuine zero-match search/export remains distinguishable from an
      operation failure.
- [x] An opt-in live IMAP test verifies two existing INBOX UIDs separately and
      together without logging message content or account-specific identifiers.
- [x] `go test ./...` passes.
