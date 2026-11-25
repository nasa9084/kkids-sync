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
kkids-sync-web member add -name 母 -email MOM -kids-profile amzn1.account.…
kkids-sync-web member add -name 妹 -email SISTER -kids-profile amzn1.account.…
kkids-sync-reconciler -web http://WEB:8081 sync   # with KKIDS_INTERNAL_TOKEN set
kkids-sync-web bootstrap                          # selections := today's shares
kkids-sync-reconciler -web http://WEB:8081 plan   # should show no changes
```

`bootstrap` refuses to run twice or before the first sync.
