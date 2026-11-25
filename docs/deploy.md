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
profiles, and then refuses the plan (exit code 4): nobody has selected
anything yet, so it would remove every share. Run `kkids-sync-web bootstrap`
at that point.

Exit codes: 3 when the Amazon session has expired, 4 when the plan has more
changes than `-max-changes`. Both also show up in the admin page's job
history.

## The Amazon session

The session file (`storage-state.json`) contains live cookies: keep it out of
Git, images and logs. The cluster has no screen to sign in with, so sign in
on a desktop and copy the file over:

```sh
make reconciler-login                        # on the Mac
# copy .poc/storage-state.json to the reconciler's /data, mode 0600
```

Do the same again when the job exits with code 3.
