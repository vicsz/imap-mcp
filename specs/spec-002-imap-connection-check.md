# spec-002 — IMAP Connection Check

Status: done
Type: feature

## Outcome

The MCP securely connects to Apple iCloud IMAP at `imap.mail.me.com:993`, authenticates with environment credentials, runs `CAPABILITY`, and returns a sanitized result.

## Constraints

- `IMAP_USERNAME` and `IMAP_PASSWORD` are required.
- The root `.env` is a temporary development source for those variables.
- No mailbox is selected and no mail content is fetched.
- Errors do not expose credentials or server transcript data.

## Done when

- [x] Configuration and dotenv parsing tests pass.
- [x] `go test ./...` passes.
- [x] A private iCloud connection check succeeds using the root `.env`.
