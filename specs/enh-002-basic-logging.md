# enh-002 — Basic logging

Status: done
Type: enhancement

## Outcome

Make IMAP activity and failures easy to inspect in macOS Console: which
operation ran, how long it took, and whether it failed.

## Constraints

- Use macOS Unified Logging with subsystem `local.imap-mail-mcp` and categories
  `imap` and `server`. On other platforms, write the same safe events to stderr.
  Never log to stdout, which carries MCP messages.
- Emit one completion event for each IMAP client operation attempted by the
  server, including connection, login, list, select, search, fetch, move, and
  append operations when used. Record the operation name, elapsed
  milliseconds, and success or a fixed error code. Do not log separate start
  events or streamed chunks.
- Use Apple's `default` level for successful operations and `error` level for
  failed operations, timeouts, and cancellations.
- Log startup failures, unexpected server failures, and per-item failures
  returned by a tool. Summarize repeated per-item failures with a count.
- Never log credentials, mail content or metadata, IMAP arguments or responses,
  message identifiers, mailbox names, search terms, file paths, or raw errors.
- Logging must not change the result of a mail operation. Let macOS manage log
  storage and retention; do not add log files, rotation, or a log-viewing tool.

## Done when

- [x] A test records the operation name and nonnegative duration for a
      successful IMAP operation and for a failed or timed-out operation.
- [x] Tests confirm per-item failures and startup failures produce error-level
      events without exposing distinctive secret or mail test values.
- [x] Tests confirm logging never writes to stdout or changes tool results.
- [x] A private Mac check finds a successful IMAP operation and a controlled
      failure in Console under `local.imap-mail-mcp`, with the expected levels.
- [x] `go test ./...` passes.
