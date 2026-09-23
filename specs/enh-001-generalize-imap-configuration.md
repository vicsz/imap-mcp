# enh-001 — Generalize IMAP configuration

Status: done
Type: enhancement

## Outcome

Allow the MCP server to connect to configurable TLS IMAP endpoints while
retaining Apple iCloud as the default, so the server is not tied to one
provider.

## Constraints

- Read `IMAP_HOST`, defaulting to `imap.mail.me.com`.
- Read `IMAP_PORT`, defaulting to `993`.
- Read `IMAP_USERNAME` and `IMAP_PASSWORD` from the environment.
- Read optional `IMAP_TLS_SERVER_NAME`, defaulting to `IMAP_HOST`.
- Keep implicit TLS required; plaintext IMAP and OAuth are outside this MVP.
- Keep mailbox selection hardcoded to `INBOX` for now. Mailbox selection is
  outside this enhancement and is not an environment variable.
- Preserve sanitized errors and never log credentials or mail transcripts.

## Done when

- [x] Configuration loads the endpoint and credentials from the environment
      with the stated defaults.
- [x] Existing Apple iCloud behavior continues to work without new settings.
- [x] Tests cover non-Apple endpoint configuration without exposing
      credentials.
- [x] Mail operations continue to select `INBOX` without requiring mailbox
      configuration.
- [x] A live Apple test still passes using the defaults.
- [x] `go test ./...` passes.
