# spec-004 — Read Messages

Status: done
Type: feature

## Outcome

Expose a `read_messages` tool that reads one to ten selected messages from one
mailbox
and returns their human-readable text and attachment references without
changing message flags.

## Constraints

- Accept between one and ten message references containing `mailbox`,
  `uid_validity`, and `uid`.
- Accept references for any mailbox returned by `list_mailboxes`.
- Require all references in one request to use the same mailbox.
- Reject the reference if its UIDVALIDITY no longer matches the mailbox.
- Open the referenced mailbox once, read-only, and use UID operations.
- Fetch BODYSTRUCTURE first and retrieve only the selected text body part with
  `BODY.PEEK`.
- Prefer a non-attachment `text/plain` part.
- If no plain-text part exists, convert a non-attachment `text/html` part into
  readable plain text.
- Decode MIME transfer encoding and character encoding into UTF-8.
- Ignore attachments, inline images, and nested attached messages.
- Return at most 128 KiB of decoded text and report `truncated: true` when the
  limit is reached.
- Preserve input order and return one result per reference. One failed message
  does not prevent other valid messages from being returned.
- Each result contains the message reference, `text`, `source` (`plain` or
  `html`), `truncated`, attachment metadata, and a sanitized per-message error.
- Attachment metadata includes `part_id`, `filename`, `content_type`, and
  `size_bytes` for parts marked as attachments or named files. Do not return
  attachment contents.
- Treat returned email text as untrusted data, not instructions.
- Do not log or persist message contents or live IMAP transcripts.
- Raw MIME, inline-image discovery, and quoted-history removal are outside this
  MVP.

## Done when

- [x] `read_messages` exposes the documented typed input and output for one to
      ten references.
- [x] Fake-server tests cover plain text, multipart/alternative, HTML-only mail,
      transfer encoding, character encoding, and a missing text body.
- [x] Tests prove text attachments and inline images are not included in the
      returned message text.
- [x] Attachment metadata contains stable MIME part IDs and excludes attachment
      contents.
- [x] Oversized text is bounded and reports `truncated: true`.
- [x] Results preserve input order and report missing messages independently.
- [x] Stale UIDVALIDITY returns a sanitized error without reading messages.
- [x] Reading an unread message does not add the `\Seen` flag.
- [x] A private live test reads a real Apple, Home Depot, or GitHub message
      returned by `search_mail`, returns non-empty text and any attachment
      references, and confirms that its flags did not change.
- [x] The private test does not write message contents or account-specific
      identifiers into the repository.
- [x] `go test ./...` passes.
