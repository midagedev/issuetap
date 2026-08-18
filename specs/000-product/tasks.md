# Tasks: Issuetap v0

- [x] Store, fixtures, JQL, CQL, locale, dialect
- [x] Cloud v3 + Confluence Cloud surface (gadak call set)
- [x] DC v2 read path
- [x] Writes (comment, transition, assignee, create, attach)
- [x] Faults + shipped scenarios
- [x] Dashboard + diagnose
- [x] Contract tests (≥2 assertions per clause)
- [x] Gadak conformance test
- [x] Family docs and CI
- [x] Secretscan
- [x] Public embedding surface (`issuetap.NewEmbedded`, root package)
- [x] Write-through persistence (`--persist`, debounced atomic writes, restart reload)
- [x] Attachment bytes survive snapshot/restore (`text` inline / `dataBase64`)
- [x] Confluence page version history (`GET /wiki/rest/api/content/{id}/version`)
- [x] Wiki writes (`POST/PUT /wiki/rest/api/content`) with optimistic concurrency and persistable history
