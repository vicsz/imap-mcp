# enh-003 — Use IMAP SORT for faster search results

Status: done
Type: enhancement

## Outcome

On servers that support IMAP `SORT`, preserve the current newest-first
`search_mail` ordering while fetching message metadata only for the requested
results. Keep the existing behavior on servers without `SORT`.

## Constraints

- Do not change `search_mail` inputs, outputs, limits, or filter semantics. Do
  not add pagination.
- After login, explicitly request capabilities. A capability-discovery failure
  is a sanitized search error; only a successfully retrieved capability list
  without `SORT` selects the existing fallback. Treat `SORT` and `SORT=DISPLAY`
  as support for base `SORT`; `ESORT` alone is not sufficient.
- When supported, reuse the existing typed search criteria with the library's
  `UID SORT` operation and `ARRIVAL` criterion. Do not construct raw IMAP search
  syntax or change filter semantics. Reverse the complete ascending sorted UID
  list to preserve internal-date-descending order and UID-descending ties, then
  retain that ordered UID slice and select up to `limit` UIDs. A UID set is not
  an ordering container.
- Set `truncated` when the complete `SORT` result contains more than `limit`
  UIDs. Fetch compact metadata only for selected UIDs, then assemble the result
  in selected-UID order even if FETCH responses arrive in another order. An
  empty result sends no metadata FETCH.
- If a selected UID is absent from a successful FETCH because the mailbox
  changed concurrently, omit it without fetching a replacement. Keep
  `truncated` based on the original `SORT` match count; this feature does not
  promise a mailbox snapshot. A FETCH command failure remains a sanitized
  search error.
- When a successful capability query does not advertise `SORT`, retain the
  current `UID SEARCH` and local-sort path. If the server advertises `SORT` but
  the `SORT` command fails, return a sanitized search error; do not silently
  perform an expensive fallback.
- Continue using read-only mailbox selection and UID operations. Do not fetch
  bodies or change message flags.
- Record capability-discovery and `SORT` duration/failure through the existing
  privacy-safe IMAP logging with fixed operation names. Do not add correlation
  IDs or log mailbox names, UIDs, search terms, or raw server errors.
- This reduces metadata fetching; it does not promise to limit the number of
  matching UIDs returned by IMAP or guarantee a particular elapsed-time
  improvement. Connection and login costs remain, and body/text criteria still
  execute on the server.

## Done when

- [x] A scripted protocol test advertises `SORT` and proves the fast path is
      used. It asserts the exact selected UIDs in the metadata FETCH, returned
      order, metadata-fetch bound, and `truncated` for zero, `limit`, and
      `limit+1` matches. An empty result issues no metadata FETCH.
- [x] SORT-path tests cover equal internal dates at the result boundary,
      numeric UIDs that confirm the tie order, FETCH responses arriving out of
      order, and a selected UID disappearing before FETCH.
- [x] A SORT-path encoding test covers the existing typed filters, including
      disjoint UID ranges combined with another filter and non-ASCII search
      text, without duplicating the full validation suite.
- [x] Tests cover a successful capability response with no SORT, capability
      discovery failure, advertised SORT rejection, and safe operation logging.
      Failures do not expose server text, mailbox names, UIDs, or search terms
      in the MCP result or logs, and an advertised SORT failure does not fall
      back to SEARCH.
- [x] A private live check establishes whether the configured account
      advertises and exercises `SORT`, without recording message identifiers or
      content in the repository. If unsupported, record that fallback works
      and that live fast-path verification is unavailable; do not count the
      fallback as SORT-path acceptance.
- [x] `go test ./...` passes.
