# 0001 — Project shape

Issuetap is a separate repository, a single Go binary, with an embedded
Svelte dashboard. Storage is SQLite (process-local `:memory:`, or an
on-disk WAL file when persist is armed). YAML is the authored fixture
and Snapshot export format because scenarios sit next to fixtures and
both siblings write scenarios in YAML; JSON is accepted so snapshots and
generated documents load.

Cloud v3 is the v0 requirement (gadak's call set). Data Center v2 is the
read path so a DC client can be developed; it is a model, not a verified
product.
