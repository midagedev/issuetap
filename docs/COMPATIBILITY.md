# Compatibility

Issuetap is a fixture-backed Atlassian lab for local development and CI.
Its goal is measurable compatibility for a documented subset of Jira Cloud
v3 and Confluence Cloud, plus a Data Center v2 **read** path so a DC client
can be developed against it.

It is not Jira. It is not Confluence. A dialect built from published
documentation is still a model, not the product. The DC path makes a Data
Center integration *buildable and regression-tested*; it does **not** make
it *verified*. Anyone shipping DC support still needs one real instance.

Anything outside this document should be treated as unsupported until it
has a fixture, a test, and an explicit compatibility note.

Known Atlassian routes that are not implemented return HTTP 501 with:

```json
{
  "errorMessages": ["issuetap does not implement GET /rest/api/3/dashboard"],
  "errors": {"endpoint": "unsupported_endpoint"},
  "issuetap": {"code": "unsupported_endpoint", "method": "GET", "path": "/rest/api/3/dashboard"}
}
```

A 404 would look like a client bug. A 200 with an invented payload would
look like the endpoint works. `unsupported_endpoint` is the honest gap.

The live inventory is `GET /api/compatibility` and
`internal/api/registry.go` (`Inventory()`). The table below matches that
list. Every **Supported** / **Partial** clause has a test in
`internal/api/contract_test.go`.

## Compatibility Levels

| Level | Meaning |
| --- | --- |
| Supported | Implemented and covered by automated tests. |
| Partial | Useful for smoke tests; not a full provider behaviour model. |
| Unsupported | Known route; returns `unsupported_endpoint`. |
| issuetap | Lab API, not intended to match Atlassian. |

## Cloud v3 (required for v0)

Base path: `/rest/api/3`. Confluence Cloud: `/wiki/rest/api`.

Identities are `accountId`. Issue and comment bodies are ADF. Search is
`POST /search/jql` with `nextPageToken` / `isLast`. Timestamps use the
Jira layout `2006-01-02T15:04:05.000-0700`.

| Area | Endpoints | Level | Scope |
| --- | --- | --- | --- |
| Myself | `GET /myself` | Supported | Credential probe. |
| Server info | `GET /serverInfo` | Supported | `serverTitle` is `issuetap`; `deploymentType` is Cloud. |
| Status | `GET /status` | Supported | Localized names; `statusCategory.key` stable. |
| Priority | `GET /priority` | Supported | Most-urgent first. |
| Issue type | `GET /issuetype` | Supported | `hierarchyLevel` present. |
| Resolution | `GET /resolution` | Supported | |
| Issue link type | `GET /issueLinkType` | Supported | Cloud default 4: Blocks, Cloners, Duplicate, Relates (ids 10000–10003). |
| Field catalog | `GET /field` | Supported | Names localize. |
| Projects | `GET /project/search`, `GET /project`, `POST /project`, `GET /project/{key}` | Supported | `values` / `isLast` / `total` / `startAt`. POST accepts `key` and `name`; duplicate or invalid key is HTTP 400. |
| Search | `POST /search/jql` | Supported | JQL subset in `internal/jql`. |
| Count | `POST /search/approximate-count` | Supported | Equals search length. |
| Issue | `GET /issue/{key}` | Supported | `expand=changelog`. |
| Changelog | `GET /issue/{key}/changelog` | Supported | `values` / `total` / `isLast`. |
| Comments | `GET /issue/{key}/comment` | Supported | `startAt` / `maxResults` / `total`. Stored `visibility` (`type` role or group + `value`) and `jsdPublic` are echoed; both keys are omitted when unset. |
| Filters | `GET /filter/my` | Supported | |
| Users | `GET /user/search` | Supported | `query=me` is the `/myself` identity. |
| Transitions | `GET/POST /issue/{key}/transitions` | Supported | Synthetic transitions to every other status. GET `?expand=transitions.fields` includes a `fields` object per transition (`{}` when the destination has no screen). POST stores `fields.resolution` by catalog id when the destination screen declares it; a field that is not on that screen is HTTP 400 `errors.<field>`. A required resolution omitted from the body is HTTP 400 `errors.resolution`. Entering done with no resolution (and none required) still fills `10000`. Leaving done clears resolution. `update.comment[].add.body` is stored as a comment. |
| Claim | `POST /issue/{key}/claim` | Supported | **issuetap extension — Atlassian has no such route** (a real-Cloud client falls back to assignee + transition calls). One atomic mutation: assignee = the request actor (`X-Issuetap-Actor`, never a body field) plus the in-progress transition. Body: optional `transitionId` (omitted → the first destination whose `statusCategory.key` is `indeterminate`; none → HTTP 400 `no in-progress transition available`) and `takeOver` (default false). Already in progress and held by another account → HTTP 409 `<KEY> is already claimed by <displayName>`. Same actor → 200 idempotent (no re-transition, no duplicate changelog row). 200 body: `{key, assignee, status, claimedAt}`; `claimedAt` is read from the changelog, not a new stored field. |
| Edit meta | `GET /issue/{key}/editmeta` | Partial | summary, description, labels, priority (+allowedValues), assignee, duedate, parent, issuetype (+allowedValues), fixVersions/components (+allowedValues from the project's issue-derived catalog), plus fixture custom fields (kind schema; option kinds include allowedValues). Not per-screen field config. |
| Create meta | `GET /issue/createmeta` | Partial | projects + types. |
| Create meta fields | `GET /issue/createmeta/{projectIdOrKey}/issuetypes/{id}` | Partial | fields list + startAt/maxResults/total. required/hasDefaultValue derived from CreateIssue (project, summary, issuetype, reporter required; issuetype/reporter/priority have defaults). Optional: description, labels, assignee, duedate, parent, fixVersions, components, plus fixture custom fields. Not per-screen field config. |
| Writes | `POST /issue`, `PUT /issue/{key}`, `POST …/comment`, `PUT …/assignee`, `POST …/attachments`, `POST /issueLink` | Supported | Mutate the in-memory graph. Parent, when set, must exist and be exactly one `hierarchyLevel` above the child (type id, not name). Create 400: `errors.parent` and `errors.parentId`. Edit 400: `errors.pid`. POST and PUT `fields.fixVersions` / `fields.components` is a full replace (same resolver); `update.fixVersions` / `update.components` accepts `add`/`remove`/`set` with `{id}` or `{name}`. Identity is the project's issue-derived catalog (fixtures have no project version/component list). Unknown id/name is HTTP 400 `errors.fixVersions` / `errors.components`. Stored on the typed arrays, not `Custom`. A project with an empty catalog accepts a name as-is. POST comment stores `visibility` (`type` must be role or group, `value` non-empty; no role/group existence check) and maps `properties` `sd.public.comment` `internal` to `jsdPublic` (`!internal`). Invalid `visibility.type` is HTTP 400 `errors.visibility`. POST `/issueLink` writes one element onto both issues (outward sees `outwardIssue`, inward sees `inwardIssue`). Type is `{id}` or `{name}` against the GET catalog; unknown type is HTTP 404 `No issue link type …`. Missing issue is HTTP 404. Self-link is HTTP 400. Duplicate same type/pair/direction is idempotent 201 (no extra element). GET `issuelinks[].type` includes `id`/`inward`/`outward` from that catalog when the stored name matches. |
| Attachments | `GET /attachment/{id}`, `GET /attachment/content/{id}` | Supported | 302 to `/file/{uuid}/binary?name=`; the target serves the stored bytes. |
| Dev status | `GET /rest/dev-status/{v}/issue/summary`, `GET /rest/dev-status/{v}/issue/detail`, `POST /rest/dev-status/{v}/issue/link` | Supported | Cloud internal development panel. `{v}` is `latest` or `1.0`. Summary counts stored pull-request, build, and deployment links (build/deployment blocks fill the captured keys only). Detail requires `applicationType` and `dataType` (Cloud's 500 param shape) and serves pull requests; Cloud's detail row vocabulary for builds/deployments was never captured, so those dataTypes serve an empty `detail`. POST upserts one link by URL — `kind` selects `pullrequest` (default), `deployment` (environment + state), or `build` (state, plus url or number). Other `/rest/dev-status/*` subpaths are `unsupported_endpoint` (501). |
| Spaces | `GET /wiki/rest/api/space`, `GET /wiki/rest/api/space/{key}` | Supported | |
| CQL | `GET /wiki/rest/api/content/search` | Supported | `space`, `type`, `lastModified`; `_links.next`. |
| Page | `GET /wiki/rest/api/content/{id}` | Supported | `body.atlas_doc_format`. |
| Page versions | `GET /wiki/rest/api/content/{id}/version` | Supported | Newest-first (`number` desc). `start`/`limit`; `_links.next` uses the same `next=true&cursor=` convention as content/search. Each row has `by`, `when`, `message`, `number`, `minorEdit`. |
| Page comments | `GET /wiki/rest/api/content/{id}/child/comment` | Supported | |
| Wiki writes | `POST /wiki/rest/api/content`, `PUT /wiki/rest/api/content/{id}` | Supported | Create is version 1. Update requires `version.number == current+1` (409 otherwise). `version.message` is stored and served by `/version`. |

JQL subset: `project`, `key`, `updated`, `created`, `status`,
`statusCategory`, `issuetype`/`type`, `priority`, `assignee`, `reporter`,
`fixVersion`, `component`,
`AND`/`OR`/`NOT`, `IN`, `ORDER BY`. Unparseable JQL is HTTP 400 — the
server does not silently return every issue.

CQL subset: `space`, `type=page|comment`, `lastModified >=`, `ORDER BY`.
Unsupported clauses are HTTP 400.

## Data Center v2 (read path)

Base path: `{context}/rest/api/2` (context defaults to empty; set
`--context-path /jira`). Auth is Bearer PAT. Identities are
`username`/`userKey`. Issue bodies are wiki-markup strings. Search is
`POST /rest/api/2/search` with `startAt` / `maxResults` / `total`.
Confluence is `{context}/rest/api` with `body.storage` XHTML.

This is enough to develop a DC client. It is not a verified DC product.

## Unsupported (honest 501)

`GET /dashboard`, `GET /board`, `GET /rest/agile/1.0/board`, webhooks,
permissions, application-properties, group/member, JQL autocomplete,
expression eval, Confluence `/wiki/api/v2/pages`, `/wiki/rest/api/user/current`,
`GET /project/{key}/versions`, `GET /project/{key}/components`.

A path after `/project/{key}` is an unimplemented sub-resource (HTTP 501),
not a missing project. `GET /project/TAP/versions` must not 404 as
`key 'TAP/versions'`.

## Lab API

`/healthz`, `/api/overview`, `/api/requests`, `/api/data`, `/api/diff`,
`/api/compatibility`, `/api/diagnostics`, `/api/fixtures/apply`,
`/api/fixtures/snapshot`, `/api/scenarios/run`.

Every response includes `X-Issuetap: 1`.
