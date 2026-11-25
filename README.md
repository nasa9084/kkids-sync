# kkids-sync

A small self-hosted portal ("ほんだな") that lets family members pick which books
appear in their Amazon Kids profiles on Kindle, without giving them access to the
owner's Amazon account.

- Family members choose books on a simple web page.
- A reconciler compares their choices with what is currently shared in the Amazon
  Kids parent dashboard and adds or removes only the difference.
- The reconciler is the only component that holds the Amazon session.

See [docs/poc.md](docs/poc.md) for the PoC (and how to remove what it installs), [docs/web.md](docs/web.md) for running it locally, and [docs/deploy.md](docs/deploy.md) for the container images.

> This project automates the owner's own account through a browser. It is not
> affiliated with or endorsed by Amazon.
