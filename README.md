# kkids-sync

A small self-hosted portal ("ほんだな") that lets family members pick which books
appear in their Amazon Kids profiles on Kindle, without giving them access to the
owner's Amazon account.

- Family members choose books on a simple web page.
- A reconciler compares their choices with what is currently shared in the Amazon
  Kids parent dashboard and adds or removes only the difference.
- The reconciler is the only component that holds the Amazon session.

Status: proof of concept. Nothing here is ready for use. See [docs/poc.md](docs/poc.md) to run the PoC (and to remove everything it installs).

> This project automates the owner's own account through a browser. It is not
> affiliated with or endorsed by Amazon.
