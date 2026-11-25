# kkids-sync-web

Serves ほんだな and owns the database. Configuration is in the package
comment of `cmd/kkids-sync-web`.

## Internal API

The reconciler talks to web over a separate listener
(`KKIDS_INTERNAL_LISTEN`, default `:8081`) with
`Authorization: Bearer $KKIDS_INTERNAL_TOKEN`. Calls only go from the
reconciler to web.

| Method | Path | |
| --- | --- | --- |
| POST | `/internal/v1/snapshot` | record a complete snapshot from `kkids-sync-reconciler sync` |
| GET | `/internal/v1/plan` | per kids profile, ASINs to add and to remove |

The plan is `desired − shared` to add and `shared − desired` to remove,
where desired is what the member selected, still in the library and visible
(book setting, then series default, then the global default, which is
hidden). Shares of titles outside the library, such as Kindle Unlimited
books, are never removed.

## First run

Everything is shared today and nobody has selected anything yet, so the
first plan would remove every book. Before any reconcile that writes:

```sh
kkids-sync-web member add -name 自分 -email OWNER -role admin
kkids-sync-web member add -name 母 -email MOM -kids-profile-name MOM_PROFILE
kkids-sync-web member add -name 妹 -email SISTER -kids-profile-name SISTER_PROFILE
kkids-sync-reconciler -web http://WEB:8081 link-profiles   # with KKIDS_INTERNAL_TOKEN set
kkids-sync-reconciler -web http://WEB:8081 sync
kkids-sync-web bootstrap                          # selections := today's shares
kkids-sync-reconciler -web http://WEB:8081 plan   # should show no changes
```

`-kids-profile-name` is the profile name as the parent dashboard shows it.
`link-profiles` selects each child in the dashboard's filter, reads which
profile ID the page then asks for, and stores it for the member with that
name; `member list` shows the result. `apply` does the same on its own when
a member has a name but no ID yet. `-kids-profile amzn1.account.…` still
sets the ID directly.

To fix a member later, `member edit -id N` with any of `-name`, `-email`,
`-role`, `-kids-profile-name` or `-kids-profile` changes just those fields;
selections stay. A new `-kids-profile-name` clears the ID so it is linked
again.

`bootstrap` refuses to run twice or before the first sync.

## Trying it locally

Two terminals, from the repository root (everything stays under `.poc/`):

```sh
# terminal 1: web in development mode, acting as WEB_EMAIL
make web-dev WEB_EMAIL=owner@example.com

# terminal 2
make web-cli ARGS="member add -name 自分 -email owner@example.com -role admin"
make web-cli ARGS="member add -name 母 -email mom@example.com -kids-profile-name MOM_PROFILE"
make web-cli ARGS="member add -name 妹 -email sister@example.com -kids-profile-name SISTER_PROFILE"
make reconciler-link              # profile names → IDs from the dashboard
make web-cli ARGS="member list"   # check both are linked
make reconciler-sync-web          # library and share state into web
make web-cli ARGS="bootstrap"     # today's shares become selections
make reconciler-plan              # should show no changes
```

Open http://127.0.0.1:8080/admin/ as the owner. To see a family page,
restart `make web-dev WEB_EMAIL=mom@example.com`.

After picking or removing a book as a family member:

```sh
make reconciler-plan              # what would change
make reconciler-apply             # change it on the dashboard, then sync
```

`apply` selects one child by name through the dashboard's filter (and
checks the ID the page asks for), searches each
book by title and presses its toggle. The guard lets through only the one
write it expects (that child, that book, that direction) and blocks
anything else. At the end it clears the filter again.
Plans larger than `MAX_CHANGES` (default 50) are refused
with exit code 4.
