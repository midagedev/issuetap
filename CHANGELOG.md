# Changelog

## Unreleased

- Attachment bytes live beside the database, not inside it. A file-backed
  store writes one content-addressed file per distinct attachment under
  `<persist-dir>/blobs/<sha[0:2]>/<sha256>`; `attachment_blobs` holds the
  reference and the path is always computed, so moving a workspace
  directory still works. Uploads stream to disk and downloads stream from
  it — nothing is buffered whole, and `/file/{uuid}/binary` answers Range
  from the file itself, so seeking in a video works. A `:memory:` store
  keeps the old BLOB path.
- Persist schema 1 → 2, the first migration this file has had: opening a
  v1 database moves its attachment bytes out and keeps the pre-migration
  copy at `<persist>.pre-v2.bak`. It takes that copy with `VACUUM INTO`
  (the persist is WAL, so copying the `.db` alone loses committed pages),
  writes the bytes before opening its transaction, and re-reads
  `user_version` inside `BEGIN IMMEDIATE` so two processes opening the
  same file migrate it once. A database from a newer build is still
  refused, now naming that copy.
- The upload cap is configuration: `--max-attachment-bytes`,
  `ISSUETAP_MAX_ATTACHMENT_BYTES`, `EmbeddedConfig.MaxAttachmentBytes`.
  Default 1 GiB (was a hardcoded 32 MiB) — the bytes no longer sit in
  memory, but an origin reached over a network still has a disk to fill.
  Negative removes the cap. Over the cap is a 413, never a truncation.
- `/file/{uuid}/binary` resolves through an index instead of scanning
  every issue in the store; the ETag is the content hash.
- Comments can be corrected and removed: `PUT /issue/{key}/comment/{id}`
  replaces the body, `DELETE` removes the comment (204). PUT keeps the id,
  author, `created`, visibility and `jsdPublic`, and moves `updated`; a
  plain-string body is stored the way `POST …/comment` stores one, since
  posting and editing now share one body normalizer. An unknown comment id
  or issue key is 404 on both. Before this the tracker was add-only, and a
  comment posted by mistake — most likely by an agent — had no way back
  (gadak GDK-1647).
- Public embedding surface: `issuetap.NewEmbedded` (root package) serves
  the full surface in-process with fixture seeding (path or bytes),
  `Snapshot()` export, and `Close`. No internal types in the API.
- On-disk SQLite persistence: `--persist <file>` (serve) or
  `EmbeddedConfig.PersistPath` names a WAL database (recommended `.db`).
  Mutations commit before return; a restart reopens the file. YAML is
  fixture seed and `Snapshot()` export only. Legacy YAML PersistPath is
  refused (pass it as FixturePath). `PersistDebounce` is a no-op. Restart
  still re-seeds id sequences and advances the deterministic clock past
  the loaded rows.
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
