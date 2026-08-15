# Plan: Issuetap v0

1. In-memory store + YAML fixtures + deterministic ids/timestamps.
2. Cloud v3 handlers matching gadak's client (`internal/jira`,
   `internal/confluence`).
3. JQL / CQL subsets; unparseable queries are 400.
4. Locale overlay on status / priority / type / field / changelog names.
5. DC v2 read path (wiki-markup, startAt, Bearer, context path).
6. Fault engine + scenario runner.
7. Svelte dashboard + diagnose zip.
8. Gadak conformance test as the acceptance gate.
9. Family docs, Makefile, CI, secretscan.
