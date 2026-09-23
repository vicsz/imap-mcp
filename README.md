# IMAP Mail MCP

A small local Go server that gives an MCP client practical tools for working
with an IMAP mailbox. It uses MCP over standard input/output, defaults to
Apple's IMAP endpoint, and accepts other IMAP endpoints through environment
variables.

## Tools

- `check_imap_connection` and `list_mailboxes` inspect the IMAP connection and
  account.
- `search_mail` and `read_messages` find and read selected messages without
  marking them as read.
- `download_attachments` and `export_messages` save selected mail data to a
  local destination.
- `move_messages` moves explicitly selected messages between mailboxes.
- `create_draft` creates a new plain-text draft or reply draft; it never sends
  mail.
- `hello` is a simple server check.

## Setup

Use Go 1.25 or newer. Set the required credentials in the MCP host environment
or in a root `.env` file:

```dotenv
IMAP_USERNAME=you@example.com
IMAP_PASSWORD=your-app-password
# Optional sender address for drafts; defaults to IMAP_USERNAME.
IMAP_FROM_ADDRESS=you@example.com
```

The root `.env` is optional, ignored by Git, and loaded when the server starts
from the project directory. Process environment variables take precedence.
For iCloud, use a revocable Apple app-specific password, not your main Apple
Account password. Keep `.env` owner-readable only (`chmod 600 .env`), and do
not paste credentials into chat or an MCP registration command.

The server can be launched by an MCP host as a local stdio server with:

```text
command: go
arguments: run ./cmd/imap-mail-mcp
working directory: /path/to/imap-mcp
```

For Codex across multiple local projects, build a stable executable and
register the included launcher once:

```sh
go build -o bin/imap-mail-mcp ./cmd/imap-mail-mcp
codex mcp add apple-mail-local -- /absolute/path/to/imap-mcp/run-mcp.sh
codex mcp list
```

The launcher changes to this project directory before starting the server, so
the same `.env` works regardless of the active Codex project's working
directory. Rebuild the executable after changing server code. This is a local
Codex stdio connection; it does not by itself connect ChatGPT on the web or
another device to your Mac.

For other providers, set `IMAP_HOST`, `IMAP_PORT`, and optionally
`IMAP_TLS_SERVER_NAME`. Defaults are `imap.mail.me.com`, `993`, and the
configured host, respectively.

## Data handling

Search and read operations are read-only. Moving messages changes the mailbox.
Downloads and exports write mail data to the destination you provide; keep
those files and credentials private. `create_draft` adds an unsent draft to the
account's server-reported Drafts mailbox; it never sends mail.

## Development

```sh
go test ./...
go vet ./...
go build ./...
```

Real-account integration tests are opt-in; see [AGENTS.md](AGENTS.md) for the
command and details.

## License

MIT. See [LICENSE](LICENSE).
