# Architecture

Issuetap is one Go binary with an embedded Svelte dashboard.

```
cmd/issuetap          serve / fixtures / scenario / diagnose
embedded.go           public embedding surface (package issuetap)
internal/
  api                 HTTP: Cloud v3, DC v2, /wiki, /api lab surface
  store               in-memory graph, snapshot/restore, opt-in persistence
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

In-memory. Snapshot/restore is a fixture document. There is no database.
Determinism is the point; durability is opt-in: `--persist <file>` (or
`EmbeddedConfig.PersistPath`) saves every mutation to one YAML file
written atomically (same-directory temp file + rename) and reloads it on
the next start. `serve --persist` writes before the HTTP response returns;
a debounced quiet window is lab-only (`--persist-debounce`, or a positive
`PersistDebounce` in embed). A restart also re-seeds the id sequences and jumps the
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
