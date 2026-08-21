# Product Specification: Issuetap

## Status

v0

## Summary

Issuetap is a local, deterministic Atlassian-compatible server for
developing and testing Jira and Confluence clients. It serves Cloud v3
(complete for gadak's call set) and a Data Center v2 read path, with
fixture apply/snapshot, first-class fault scenarios, localized display
names, a Svelte dashboard, and a diagnostics bundle. The root package
`issuetap` is a public embedding contract (in-process handler with
opt-in write-through persistence) so another Go program can use it as
its origin store.

It is not an issue tracker.

## Users

- client author (gadak and anyone else talking to Jira/Confluence)
- QA writing CI scenarios
- agent diagnosing a failed sync

## Core Scenarios

### 1. Gadak syncs from issuetap

A fixture is loaded. gadak is pointed at `http://127.0.0.1:<port>` with a
throwaway profile. `gadak sync` mirrors issues, comments, changelog, and
Confluence pages (including page version history). Counts match the fixture.

### 2. Localized names break a name-keyed client

`--locale ko` serves `진행 중` / `작업`. `status = "In Progress"` is
empty. `status = 3` hits.

### 3. Credential revoked mid-run

A scenario returns 401 from request N. The client stops. A client that
retries forever fails the scenario.

### 4. Embed, mutate, restart

A Go program embeds issuetap (`issuetap.NewEmbedded`), points a client at
its handler, creates data, and shuts down. On the next start with the
same `PersistPath` the data is back: issues, comments, attachment bytes,
wiki pages, and page version history (including `version.message`).
Ids continue (no reuse) and new mutations are stamped after the restored
rows, so an `updated >=` delta sync does not skip them.

### 5. Standalone workspace edits through editmeta

A client (gadak standalone, or any other) asks
`GET /rest/api/3/issue/{key}/editmeta` what it may write. The response
lists every first-class writable system field — summary, description,
labels, priority (with `allowedValues` from the priority catalog),
assignee, duedate, parent, issuetype (with `allowedValues` from the type
catalog) — plus every custom field defined in the fixture/persist
registry. Option-shaped custom fields include `allowedValues`.
`PUT /issue/{key}` accepts every advertised field. `duedate` is a
first-class `YYYY-MM-DD` string on the issue (not a `Custom` map entry);
a malformed date is HTTP 400. A registered option field rejects an
unknown option id with HTTP 400; an unregistered custom field id still
stores freely (backward compatible). A parent, when set, must name an
existing issue whose `hierarchyLevel` is exactly one above the child's
(keyed on type id, not the localized name). Same-level and reverse
parents are HTTP 400; `errors` carries `pid` on edit. Persist/fixture
rows that already break the rule still load — diagnostics reports the
count.

The field registry is the single owner of "what is editable and which
values are allowed". editmeta and UpdateIssue both derive from it. There
is no admin UI for the registry in this version — fixtures and persist
are the source.

### 6. Standalone workspace creates through createmeta fields

A client (gadak standalone, or any other) asks
`GET /rest/api/3/issue/createmeta/{projectIdOrKey}/issuetypes/{id}`
what it must send to create. The response is a paginated `fields`
**list** (editmeta is a map — the shapes differ) with
`startAt` / `maxResults` / `total`. Each row carries `fieldId`,
`name`, `required`, `hasDefaultValue`, and `schema`.

The advertised set is derived from what `CreateIssue` actually
requires and fills:

- required, no default: `project`, `summary`
- required, filled by issuetap when omitted: `issuetype`, `reporter`
- optional, filled by issuetap when omitted: `priority`
- optional: `description`, `labels`, `assignee`, `duedate`, `parent`,
  plus every custom field in the fixture/persist registry

`POST /issue` rejects a missing or empty `summary` with Jira's
per-field 400 (`errors.summary`). Filling every advertised required
field succeeds. A missing project or issue type on the createmeta
fields URL is HTTP 404 (the route is implemented — not 501). A parent,
when set, must exist and sit exactly one `hierarchyLevel` above the
child; create 400s with `errors.parent` and `errors.parentId`. Omitting
parent stays allowed (sub-task-requires-parent is out of scope).

## Non-goals

- Being a usable issue tracker
- Full JQL / CQL
- Boards, dashboards, Forge apps
- Verified Data Center parity
- Outbound calls to a real site at runtime
