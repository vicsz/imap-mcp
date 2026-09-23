# spec-005 — Download Attachments

Status: done
Type: feature

## Outcome

Expose a `download_attachments` tool that downloads explicitly selected
attachments from messages in one mailbox into a user-provided directory without
changing message flags or overwriting existing files.

## Constraints

- Accept an existing absolute destination directory and between one and ten
  attachment references returned by `read_messages`.
- Each attachment reference contains a message reference and MIME `part_id`.
- Accept message references for any mailbox returned by `list_mailboxes`.
- Require all attachments in one request to use the same mailbox.
- Validate UIDVALIDITY and confirm that each `part_id` still identifies a
  downloadable attachment in the message BODYSTRUCTURE.
- Open the referenced mailbox read-only and fetch selected parts using UID operations and
  read-only body fetches.
- Decode MIME transfer encoding before saving the file.
- Stream attachment data instead of buffering the complete file in memory.
- Limit each attachment to 25 MiB and the complete request to 100 MiB.
- Save files outside the repository.
- Reduce supplied filenames to a safe filename and reject path traversal,
  control characters, directory separators, and symlink destinations.
- Use a deterministic fallback filename when an attachment has no usable name.
- Create files atomically with private permissions and never overwrite an
  existing file.
- Remove incomplete temporary files after failure or cancellation.
- Return one result per requested attachment containing its reference, status,
  saved path, decoded size, SHA-256 hash, and sanitized error.
- One failed attachment does not prevent other valid attachments from being
  downloaded.
- Do not execute, open, inspect, or extract downloaded files.
- Do not log attachment contents or live IMAP transcripts.

## Done when

- [x] `download_attachments` exposes the documented typed input and output.
- [x] Fake-server tests cover one attachment and a bounded multi-attachment
      request.
- [x] Tests cover base64 and quoted-printable decoding.
- [x] Tests reject stale UIDVALIDITY, invalid part IDs, non-attachment parts,
      oversized data, path traversal, symlinks, and existing destination files.
- [x] Partial failures are reported without hiding successful downloads.
- [x] Interrupted downloads leave no partial destination file.
- [x] Downloading an attachment does not add the `\Seen` flag.
- [x] A private live test downloads one real attachment to a temporary directory
      outside the repository and verifies its size and SHA-256 hash.
- [x] The private test does not retain attachment data in the repository.
- [x] `go test ./...` passes.
