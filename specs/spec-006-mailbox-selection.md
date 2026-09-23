# spec-006 — Mailbox Selection

Status: done
Type: feature

## Outcome

Discover the account's IMAP mailboxes and let mail searches and selected
message operations target a mailbox other than `INBOX`, while keeping `INBOX`
as the default when no mailbox is provided.

## Constraints

- Add a `list_mailboxes` tool that logs in and returns `mailboxes`, each with a
  `name`, `delimiter`, and server-reported `attributes`, without selecting a
  mailbox for message work.
- Add an optional `mailbox` field to `search_mail`; when omitted, use `INBOX`.
- Preserve the selected mailbox in every returned message reference.
- `read_messages` and `download_attachments` use the mailbox in each supplied
  message reference and no longer assume `INBOX`.
- Each read or download request must refer to one mailbox; reject a request
  that mixes references from different mailboxes.
- Use read-only mailbox selection and UID operations for message reads.
- Accept mailbox names as exact values returned by IMAP; reject empty names,
  control characters, and references for a different mailbox than the one
  being operated on.
- Keep mailbox names and attributes in structured results; do not return raw
  IMAP protocol data or message contents from `list_mailboxes`.
- Do not log credentials, message content, or live IMAP transcripts.
- Mailbox creation, deletion, renaming, subscriptions, and cross-mailbox
  search are outside this MVP.

## Done when

- [x] `list_mailboxes` exposes the documented structured output.
- [x] `search_mail` defaults to `INBOX` and can search an explicitly selected
      mailbox.
- [x] Message references identify the selected mailbox, UIDVALIDITY, and UID.
- [x] `read_messages` and `download_attachments` work with valid references
      from non-`INBOX` mailboxes and reject mismatched references safely.
- [x] Fake-server tests cover mailbox listing, default selection, explicit
      selection, and invalid mailbox references.
- [x] A private live test lists mailboxes and searches at least one non-`INBOX`
      mailbox without writing account-specific data to the repository.
- [x] `go test ./...` passes.
