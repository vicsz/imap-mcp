# spec-009 — Create Draft

Status: active
Type: feature

## Outcome

Expose one `create_draft` tool that creates either a new plain-text draft or a
plain-text reply draft in the account's IMAP Drafts mailbox without sending
mail.

## Constraints

- Accept a required `mode` of `new` or `reply` and a required non-empty `text`
  body of at most 128 KiB of UTF-8 text.
- Define the input as two explicit, mutually exclusive typed branches:
  - `mode: new` requires `to` and `subject`, allows optional `cc` and `bcc`,
    and rejects `reply_to`;
  - `mode: reply` requires one `reply_to` message reference and rejects
    caller-supplied `to`, `cc`, `bcc`, and `subject`.
- Accept between one and twenty total recipients for a new draft. Parse each
  recipient as one RFC 5322 mailbox, reject invalid addresses and control
  characters, and do not accept raw header text.
- Require a new-draft subject containing between one and 500 Unicode
  characters. Reject carriage returns, line feeds, and other control
  characters in the subject.
- Read the sender identity from `IMAP_FROM_ADDRESS`, defaulting to
  `IMAP_USERNAME`, and require it to be one valid RFC 5322 mailbox. Do not
  accept a caller-supplied `from` value.
- For a reply draft, require `reply_to` to contain `mailbox`, `uid_validity`,
  and `uid`. Validate UIDVALIDITY and fetch only the source headers needed to
  construct the reply without changing the source message's flags.
- Address a reply to the source message's valid `Reply-To` mailbox when
  present, otherwise to its valid `From` mailbox. Reply to the sender only;
  reply-all is outside this MVP.
- Derive the reply subject from the source subject and add `Re:` only when it
  does not already have a reply prefix. Do not accept a subject override in
  reply mode.
- Set `In-Reply-To` and `References` from valid source `Message-ID` and
  `References` headers when available. If the source has no valid
  `Message-ID`, create the draft without threading headers and report
  `threaded: false`.
- Discover the destination mailbox using the server-reported `\Drafts`
  special-use attribute. Return a sanitized failure if there is not exactly
  one usable Drafts mailbox; do not guess a mailbox name or create, rename, or
  select a Drafts mailbox supplied by the caller.
- Construct a complete RFC 5322 MIME message with a generated `Message-ID`,
  `Date`, configured `From`, derived or supplied recipients, subject, and one
  UTF-8 `text/plain` body. HTML, signatures, quoted source history, inline
  images, and attachments are outside this MVP.
- Append the message to the discovered Drafts mailbox with the IMAP `\Draft`
  flag. Do not use SMTP and do not expose any send operation.
- Return `status` (`created` or `unknown`), `mode`, the generated `message_id`,
  and `threaded` for reply mode. Include a `draft_reference` containing
  mailbox, UIDVALIDITY, and UID only when the server supplies APPENDUID
  information.
- Do not automatically retry an APPEND after a timeout, cancellation, or
  connection loss because the server may already have created the draft.
  Return `status: unknown` with the generated `message_id`; return a sanitized
  tool error for failures known to occur before the draft was created.
- Do not save draft contents locally or log credentials, recipients, subjects,
  body text, message references, raw server errors, or live IMAP transcripts.

## Done when

- [x] `create_draft` exposes one typed tool with the documented `new` and
      `reply` branches and structured output.
- [x] Validation rejects missing or conflicting mode fields, invalid sender or
      recipient addresses, header injection, empty or oversized text, and an
      invalid or oversized subject.
- [x] Fake-server tests prove new drafts are well-formed UTF-8 plain-text MIME
      messages appended to the server-reported `\Drafts` mailbox with the
      `\Draft` flag.
- [x] Fake-server tests prove reply drafts prefer `Reply-To`, fall back to
      `From`, derive the subject, preserve valid threading headers, and report
      an unthreaded draft when the source lacks a valid `Message-ID`.
- [x] Tests reject stale UIDVALIDITY and missing source messages without
      creating a draft or changing source-message flags.
- [x] Tests cover a missing or ambiguous `\Drafts` mailbox, APPEND failure,
      cancellation, absent APPENDUID data, and successful draft references.
- [ ] Tests prove the implementation never invokes SMTP, never retries an
      ambiguous APPEND automatically, and never writes draft content to local
      files or logs.
- [ ] A private live test creates one new draft and one reply draft using the
      root `.env`, verifies that both appear as editable unsent drafts in Apple
      Mail, verifies reply addressing and threading when available, and
      confirms that the source message's flags did not change.
- [ ] Private live-test output does not write draft contents, addresses,
      subjects, message references, or account identifiers into the
      repository. Test drafts may be removed manually in Mail; the test does
      not add or use permanent deletion behavior.
- [x] `go test ./...` passes.
