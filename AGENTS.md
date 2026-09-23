# IMAP Mail MCP

This is a small local Go MCP server for an IMAP account, defaulting to iCloud.

## Project rules

- Keep the server local and stdio-based.
- Keep IMAP protocol code behind `internal/imapclient`.
- The Apple endpoint defaults to `imap.mail.me.com:993`; `IMAP_HOST`,
  `IMAP_PORT`, and `IMAP_TLS_SERVER_NAME` may override it.
- Read credentials from `IMAP_USERNAME` and `IMAP_PASSWORD`; never put them in source, specs, tests, or logs.
- The root `.env` is temporary development configuration and is ignored by Git.
- Do not store mail, attachments, or wire transcripts in the repository.
- Use UIDs for message work when later specs add mail operations.
- Keep each selected feature in one short self-contained spec.

## Tests

- `go test ./...` runs offline unit and fake-server tests; it must not require
  account credentials or connect to IMAP.
- Live IMAP tests are in `internal/imapclient/live_test.go` and require the
  `integration` build tag. Run them with
  `go test -tags=integration -count=1 -run Integration ./internal/imapclient`.
- Integration tests use the local `.env` and may read real mailbox data. Keep
  their output free of message contents and account-specific identifiers. Do
  not enable the integration tag in CI.

## Work-item format

Create one file in `specs/` for a feature, enhancement, or bug. Assign the
next unused number within that work-item prefix. Feature specs keep the
existing `spec-000` sequence; bug and enhancement sequences start at `001`.
Use a distinct lowercase work-item prefix in both the filename and heading:

- `spec-NNN-short-title.md` for features.
- `bug-NNN-short-title.md` for bugs.
- `enh-NNN-short-title.md` for enhancements.

For example, the first enhancement is `enh-001`, while the next feature after
`spec-005` is `spec-006`.

```md
# spec-NNN — Short title

Status: draft | active | done
Type: feature | enhancement | bug

## Outcome
One or two plain-language sentences.

## Done when
- [ ] Observable result or regression covered.
- [ ] `go test ./...` passes.
```

Do not create separate roadmap, workflow, security, or evidence documents.
