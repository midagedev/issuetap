# Scenarios

A scenario is a YAML file that names a fixture, a dialect, a locale, and a
list of faults. `issuetap scenario run <file>` starts an ephemeral server
(or uses `--addr`), applies the faults, and runs the HTTP assertions.

```bash
go run ./cmd/issuetap scenario run examples/scenarios/credential-revoked.yaml
```

The report is JSON on stdout (`--report path` writes a copy). Exit status
is non-zero if any assertion failed.

## Fault fields

| Field | Meaning |
| --- | --- |
| `after` | 1-based matching-request index at which the fault starts |
| `times` | how many times it fires; 0 = forever |
| `method` | optional method filter |
| `pathPrefix` / `pathContains` | path filter |
| `status` | HTTP status to return |
| `retryAfter` | `Retry-After` seconds |
| `delay` | Go duration to sleep |
| `malformed` | truncated JSON |
| `truncateBytes` | cut the real body |

Counts are per matching request, deterministic, process-local.

## Shipped examples

| File | What it proves |
| --- | --- |
| `credential-revoked.yaml` | 401 from request 2 on `/rest/` |
| `rate-limit-burst.yaml` | one 429 + `Retry-After`, then 200 |
| `confluence-401-stops-watch.yaml` | `/wiki/` 401 while Jira still works — the gadak watch-loop bug |
| `locale-ko-name-trap.yaml` | name-keyed JQL is empty; id-keyed JQL hits |

On a running server, `POST /api/scenarios/run` replaces the live fault list.
