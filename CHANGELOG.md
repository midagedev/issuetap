# Changelog

## Unreleased

- Public embedding surface: `issuetap.NewEmbedded` (root package) serves
  the full surface in-process with fixture seeding (path or bytes),
  `Snapshot()` export, and `Close`. No internal types in the API.
- Write-through persistence: `--persist <file>` (serve) or
  `EmbeddedConfig.PersistPath`. Mutations are debounced (default 1s) to an
  atomic same-directory rename and reloaded on restart, which also
  re-seeds id sequences and advances the deterministic clock past the
  loaded rows.
- Attachment bytes survive snapshot/restore: printable UTF-8 content
  snapshots inline as `text:`, binary as `dataBase64:`; the
  `/file/{uuid}/binary` download target now serves the stored bytes
  instead of an empty 200.

## 0.1.0 — 2026-08-15

- Initial v0: Cloud v3 + Confluence Cloud surface gadak can sync from.
- Data Center v2 read path (model, not verified).
- `--locale ko|ja|de|en` display-name overlay.
- Fixture apply (offline validate; `POST /api/fixtures/apply` on a running server) and snapshot (`GET /api/fixtures/snapshot` or `issuetap fixtures snapshot`). Fault scenarios, Svelte dashboard, diagnose zip.
- `unsupported_endpoint` for known unimplemented routes.
- Gadak conformance test.
