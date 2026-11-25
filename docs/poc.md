# PoC: inspecting the parent dashboard

The PoC opens a real (headful) Chromium so you can sign in and browse the
Amazon Kids parent dashboard by hand while it records what the page looks like.
It never clicks anything itself, and in `inspect` mode it blocks
state-changing requests to the dashboard.

## Requirements

- Go 1.24 or later
- `make`

In `inspect` mode the dashboard's POST requests are blocked unless the
endpoint name says it only reads data (`/ajax/get-…`, `fetch-`, `list-`,
`search-`, `load-`, `query-`). Blocked requests are printed as `BLOCKED …`.
If part of the page fails to load, one of those lines is a read endpoint
that needs adding to `readOnlyPost` in `internal/guard`.

## Run

```sh
make poc-install   # downloads the Playwright driver and Chromium into .poc/tools
make poc-login     # sign in by hand, press Enter in the terminal when done
make poc-inspect   # browse to the book list by hand, press Enter when done
```

`make poc-inspect-write` is the same as `poc-inspect` but lets dashboard writes
through, so a toggle really changes what is shared. Use it only to record a
write, and turn the toggle back afterwards.

Output goes to `.poc/`:

| File | Contents | Share? |
| --- | --- | --- |
| `page-*.html`, `page-*.png` | DOM and screenshot of each open tab | yes |
| `requests-*.tsv` | one line per request, query strings removed | yes |
| `api-*-req.json`, `api-*-resp.json` | request and response bodies of `/ajax/get-additional-content` and `/ajax/search-additional-content` (the book list) | yes |
| `blocked-*.txt` | body of each blocked write request, e.g. pressing a toggle | yes |
| `write-*.txt` | body of each dashboard write let through by `make poc-inspect-write` | yes |
| `trace-*.zip` | Playwright trace (`go run github.com/mxschmitt/playwright-go/cmd/playwright show-trace`) | only if needed |
| `network-*.har` | HAR without bodies, **includes cookies** | no |
| `storage-state.json` | signed-in session, **includes cookies** | no |

## Uninstall

The Makefile points every download and cache at `.poc/tools`, so nothing is
written to `~/Library/Caches` or `~/go`.

```sh
make poc-clean-tools   # remove driver, Chromium and Go caches; keep session and recordings
make poc-clean         # remove all of .poc/, including the saved session
```

Deleting the repository clone afterwards leaves nothing behind.

If you ran `go run ./cmd/poc` directly instead of through `make`, the defaults
were used instead. Remove these on macOS:

```sh
rm -rf ~/Library/Caches/ms-playwright-go ~/Library/Caches/ms-playwright
go clean -modcache   # note: clears the whole Go module cache, not only this project
```

Signing in also leaves a session on Amazon's side. To end it, sign out from
"Your Account > Login & security" or simply let it expire.
