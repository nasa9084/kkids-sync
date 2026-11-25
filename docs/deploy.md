# Running it for real

The images are built by [.github/workflows/images.yml](../.github/workflows/images.yml):

| Image | What |
|---|---|
| `ghcr.io/nasa9084/kkids-sync-web` | `kkids-sync-web serve` on :8080 (pages) and :8081 (internal API). Data in `/data` (`KKIDS_DB=/data/kkids-sync.db`, covers in `/data/covers`). |
| `ghcr.io/nasa9084/kkids-sync-reconciler` | `kkids-sync-reconciler run` under `xvfb-run` (headful Chromium). Session and snapshot in `/data`. |

Push a `v*` tag for a versioned image; `main` gets `sha-<commit>`.

## The scheduled job

`run` is meant to be started every few minutes (one at a time):

1. wait a random time up to `-jitter`;
2. `sync` the library if the snapshot in `-out` is missing or older than
   `-sync-every` (default 24h);
3. `apply` the plan. With nothing to change and every family member linked,
   no browser is started;
4. `sync` again if apply changed something.

On a fresh install the first run fills web with the library and links the
profiles. Until `kkids-sync-web bootstrap` has run, the plan never removes
anything (nobody has selected anything yet, so it would remove every share);
run it at that point.

Each run makes at most `-max-changes` changes (default 50), removals first,
taking turns between family members; the rest follow on the next runs.

Exit code 3 means the Amazon session has expired; it also shows up in the
admin page's job history.

## The Amazon session

The session file (`storage-state.json`) contains live cookies: keep it out of
Git, images and logs. Amazon ties a session to the browser it was made in, so
a file signed in on the Mac does not work in the cluster: sign in with the
reconciler's own browser instead.

`kkids-sync-login` (in the reconciler image) starts the browser on a virtual
screen, shows it through noVNC on port 6080 with a random password printed at
start, and runs `kkids-sync-reconciler login`. Open
`http://<host>:6080/vnc.html`, sign in (including the one-time code), and it
saves the session once the parent dashboard shows (or gives up after 15
minutes). Do the same again when the job exits with code 3.

The session is saved only while the page is on the dashboard, so a run that
finds itself signed out does not overwrite the file.
