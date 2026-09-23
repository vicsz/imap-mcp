# spec-008 — Bulk Export

Status: done
Type: feature

## Outcome

Export matching messages from one or more mailboxes to a user-provided
directory, with optional extracted attachments, so mailbox backups do not
require repeated individual MCP calls.

## Constraints

- Add an `export_messages` tool.
- Accept one or more mailbox names; when omitted, use `INBOX`.
- Apply the existing typed search filters independently to each selected
  mailbox. Do not accept raw IMAP search syntax.
- Accept an existing absolute destination directory outside the repository.
- Export each matching message as a complete RFC822 `.eml` file without
  changing its MIME contents, using a safe, deterministic filename based on
  mailbox and message identity.
- Accept `include_attachments`; when true, also save decoded attachments in a
  per-message directory without overwriting existing files.
- Use mailbox, UIDVALIDITY, and UID together as message identity; the same UID
  in two mailboxes is not the same message.
- Use read-only mailbox selection, UID operations, and `BODY.PEEK`; exporting
  must not add the `\\Seen` flag.
- Continue after individual message or attachment failures and record each
  failure in the export manifest.
- Write a manifest containing the request, selected mailboxes, message
  identities, output paths, attachment paths, hashes, statuses, and errors.
- Write the manifest atomically so an interrupted export leaves a usable
  record of completed work.
- Return only a compact summary and manifest path through MCP; do not return
  message bodies or attachment contents in the tool result.
- Do not execute, open, inspect, or extract exported files after writing them.
- Do not log credentials, message content, or live IMAP transcripts.

## Done when

- [x] `export_messages` exposes the documented typed input and compact summary.
- [x] Fake-server tests cover one mailbox, multiple mailboxes, empty results,
      duplicate UIDs across mailboxes, and mixed per-message failures.
- [x] Tests verify `.eml` output, optional attachment extraction, safe
      filenames, hashes, atomic manifest writes, and no overwrite behavior.
- [x] Tests prove exports do not add the `\\Seen` flag.
- [x] A private live test exports matching mail from `INBOX` and `Archive` (or
      another available non-`INBOX` mailbox) to a temporary directory outside
      the repository and removes the temporary output afterward.
- [x] `go test ./...` passes.
