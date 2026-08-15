# Contributing

Issuetap is source-only and pre-release.

Before contributing, read:

- `README.md` for the product boundary
- `AGENTS.md` for the required-reading order
- `docs/COMPATIBILITY.md` for the supported surface
- `docs/LOCALES.md` for the name-keying trap
- `SECURITY.md` for secret handling

## Development Setup

```bash
npm install
npm run build
go test ./... -count=1
make secretscan
```

## Before Sending Changes

- keep the change scoped
- add or update tests when a compatibility clause, locale overlay, fault,
  or fixture behaviour changes
- do not write live site hosts, emails, or tokens to the tree
- do not edit gadak, billtap, or dogtap

For changes touching search, changelog, comments, or Confluence shapes,
run `make test-gadak` if gadak is available next to this repo.
