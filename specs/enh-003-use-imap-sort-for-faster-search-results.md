# enh-003 — Use IMAP SORT for faster search results

Status: draft
Type: enhancement

## Outcome

On servers that support IMAP `SORT`, return the same newest-first `search_mail`
results while fetching message metadata only for the requested results. Keep
the existing behavior on servers without `SORT`.

## Constraints

- Do not change `search_mail` inputs, outputs, limits, or filter semantics. Do
  not add pagination.
- After login, check whether the server advertises `SORT`.
- When supported, apply the existing typed filters with `UID SORT (ARRIVAL)`.
  Reverse the complete sorted UID list to preserve internal-date-descending
  order and UID-descending ties; then select up to `limit` UIDs.
- Set `truncated` when more than `limit` UIDs matched. Fetch compact metadata
  only for selected UIDs, and return messages in the selected order.
- When `SORT` is not advertised, retain the current `UID SEARCH` and local-sort
  path.
- If an advertised `SORT` command fails, return a sanitized search error; do
  not silently perform an expensive fallback.
- Continue using read-only mailbox selection and UID operations. Do not fetch
  bodies or change message flags.
- This reduces metadata fetching; it does not promise to limit the number of
  matching UIDs returned by IMAP.

## Done when

- [ ] Offline tests prove that a `SORT` search with more than `limit` matches
      fetches metadata for no more than `limit` messages and sets `truncated`
      correctly.
- [ ] Tests cover equal internal dates, fetched responses arriving out of
      order, an empty result, and a server without `SORT`.
- [ ] Tests cover an advertised `SORT` command failure without leaking server
      details.
- [ ] A private live search confirms the `SORT` path works with the configured
      account without recording message identifiers or content in the
      repository.
- [ ] `go test ./...` passes.
