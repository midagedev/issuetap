# Testing

## Gates

```bash
go build ./...
go vet ./...
go test ./... -count=1
npm run typecheck
npm run build
make secretscan
```

The gadak conformance test (`internal/conformance`) builds gadak from
`ISSUETAP_GADAK_SRC` (default `/Users/hckim/repo/gadak` when present),
points it at issuetap, runs `gadak sync --full`, and checks the mirror:

- issue count, keys, status ids
- comment rows, changelog rows
- Confluence page count

Then it revokes the credential mid-sync and asserts gadak stops, and
returns one 429 and asserts gadak retries.

```bash
make test-gadak
```

`go test ./...` skips conformance when gadak is not on disk unless
`ISSUETAP_REQUIRE_GADAK=1`.

## Contract mapping

`internal/api/contract_test.go` opens with the clause ↔ assertion table
for every row in `docs/COMPATIBILITY.md`. Each clause has a happy path
and a violation/boundary.

## Determinism

`TestDeterminism` loads the tiny fixture twice with seed 1 and diffs the
YAML snapshots. They must be identical.

## Secrets

`make secretscan` greps the tree for Atlassian token shapes, non-example
`*.atlassian.net` hosts, and `ISSUETAP_REF_*=` assignments. The live
reference-site credentials must never be written to disk.
