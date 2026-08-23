# Architecture

Issuetap is one Go binary with an embedded Svelte dashboard.

```
cmd/issuetap          serve / fixtures / scenario / diagnose
embedded.go           public embedding surface (package issuetap)
internal/
  api                 HTTP: Cloud v3, DC v2, /wiki, /api lab surface
  store               SQLite graph, YAML snapshot/restore, opt-in on-disk persist
  fixtures            YAML/JSON document
  jql / cql           the query subsets gadak actually sends
  locale              display-name overlay (ids stay put)
  dialect             Cloud vs DC path and body shape
  faults              deterministic injection
  scenarios           fixture + faults + HTTP assertions
  diagnostics         zip bundle
  config              process flags / env
web/                  Svelte lab dashboard, built into dist/app
```

## Storage

The working copy is SQLite (entity rows + JSON blobs). Without
`--persist` it is process-local (`:memory:`). With `--persist <file>`
(recommended `.db`, or `EmbeddedConfig.PersistPath`) that file is the
working copy: every mutation commits before the call returns, and a
restart opens the same database. YAML is the seed (fixture) and
`Snapshot()` export format — not the durable write path. A PersistPath
that is legacy YAML is refused; pass it as `--fixture` / `FixturePath`
and point PersistPath at a new `.db`. `PersistDebounce` is retained and
is a no-op. A restart also re-seeds the id sequences and jumps the
deterministic clock past the newest loaded timestamp, so post-restart
mutations never collide with restored ids or sort before existing rows.
Deleting the file reseeds from the fixture.

Attachment content lives in the fixture document so snapshots round-trip:
printable UTF-8 snapshots as inline `text:` (authored fixtures stay
human-readable), anything binary as `dataBase64:`.

## Request path

1. Fault engine sees method + path and may 401 / 429 / delay / truncate.
2. Atlassian routes require Authorization (Basic on Cloud, Bearer on DC).
3. Lab routes (`/healthz`, `/api/…`, the dashboard) do not.
4. Every request is appended to a bounded trace ring.

## Embedding

`npm run build` writes `dist/app`. `//go:embed all:dist/app` compiles that
into the binary, the same way gadak does.

Go programs can also embed the server itself: `issuetap.NewEmbedded` (root
package) returns an `http.Handler` over the full surface with optional
fixture seeding and persistence. No internal type appears in that API —
it is the public embedding contract, so an embedder imports only
`github.com/midagedev/issuetap`.

## No outbound network

Handlers never dial out. Avatar URLs are local `/issuetap/avatar/…` pixels.
Attachment content redirects to `/file/{uuid}/binary` on the same host,
which serves the stored bytes.
