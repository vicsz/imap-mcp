# spec-003 — Search Mail

Status: done
Type: feature

## Outcome

Expose a typed `search_mail` tool that searches a selected mailbox (defaulting
to `INBOX`) and returns a bounded list of compact message references, newest
first.

## Constraints

- Open the requested mailbox read-only and use UID operations; default to
  `INBOX` when `mailbox` is omitted.
- Accept an optional `mailbox` name returned by `list_mailboxes`.
- Accept these optional flat typed filters:
  - `since`, `before`, `sent_since`, and `sent_before`;
  - `headers`, as `{name, value}` pairs;
  - `body` and `text`, as lists of strings;
  - `with_flags` and `without_flags`;
  - `larger_than_bytes` and `smaller_than_bytes`;
  - `uid_ranges`, as `{start, end}` pairs; and
  - `limit`.
- Combine all supplied filters with AND; an empty filter searches all mail.
- Accept dates as `YYYY-MM-DD`; IMAP ignores time and timezone for these filters.
- Do not accept raw IMAP syntax or message sequence numbers.
- OR/NOT groups, MODSEQ, pagination, and caller-selected sorting are outside
  this MVP.
- `limit` defaults to 25 and must be between 1 and 100.
- Order results by internal date descending, then UID descending as a stable
  tie-breaker.
- Return `messages` and a `truncated` boolean. Each message contains `mailbox`,
  `uid_validity`, `uid`, `subject`, `from`, `sent_at`, `internal_date`, `flags`,
  and `size_bytes`. Do not return bodies or attachment data.
- Do not log credentials, message content, or live IMAP transcripts.

## Done when

- [x] `search_mail` exposes the documented typed input and structured output.
- [x] Every supported input field maps to the corresponding typed IMAP search
      criterion; user values are never inserted as raw IMAP syntax.
- [x] Fake-server tests cover unread, body, text, header, date, flag, size, and
      UID filters, including combinations of filters.
- [x] Invalid dates, limits, headers, UIDs, and control characters are rejected.
- [x] Results never exceed `limit` and use the selected mailbox, UIDVALIDITY,
      and UID.
- [x] Results are ordered by internal date descending, then UID descending.
- [x] Searching and fetching compact metadata do not change message flags.
- [x] A private live test using the root `.env` searches `INBOX` for Apple,
      Home Depot, and GitHub mail and returns at least one real message reference
      for each search, including UIDVALIDITY and UID.
- [x] The private live test may print compact results to its console but does not
      write message data or account-specific identifiers into the repository.
- [x] `go test ./...` passes.
