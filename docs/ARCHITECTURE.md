# Architecture

Issuetap is one Go binary with an embedded Svelte dashboard.

```
cmd/issuetap          serve / fixtures / scenario / diagnose
internal/
  api                 HTTP: Cloud v3, DC v2, /wiki, /api lab surface
  store               in-memory graph, snapshot/restore
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
Determinism is the point; durability is not.

## Request path

1. Fault engine sees method + path and may 401 / 429 / delay / truncate.
2. Atlassian routes require Authorization (Basic on Cloud, Bearer on DC).
3. Lab routes (`/healthz`, `/api/…`, the dashboard) do not.
4. Every request is appended to a bounded trace ring.

## Embedding

`npm run build` writes `dist/app`. `//go:embed all:dist/app` compiles that
into the binary, the same way gadak does.

## No outbound network

Handlers never dial out. Avatar URLs are local `/issuetap/avatar/…` pixels.
Attachment content redirects to `/file/{uuid}/binary` on the same host.
