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
| Field catalog | `GET /field` | Supported | Names localize. |
| Projects | `GET /project/search`, `GET /project`, `GET /project/{key}` | Supported | `values` / `isLast` / `total` / `startAt`. |
| Search | `POST /search/jql` | Supported | JQL subset in `internal/jql`. |
| Count | `POST /search/approximate-count` | Supported | Equals search length. |
| Issue | `GET /issue/{key}` | Supported | `expand=changelog`. |
| Changelog | `GET /issue/{key}/changelog` | Supported | `values` / `total` / `isLast`. |
| Comments | `GET /issue/{key}/comment` | Supported | `startAt` / `maxResults` / `total`. |
| Filters | `GET /filter/my` | Supported | |
| Users | `GET /user/search` | Supported | |
| Transitions | `GET/POST /issue/{key}/transitions` | Supported | Synthetic transitions to every other status. |
| Edit meta | `GET /issue/{key}/editmeta` | Partial | summary, description, labels, priority (+allowedValues), assignee, duedate, parent, issuetype (+allowedValues), plus fixture custom fields (kind schema; option kinds include allowedValues). Not per-screen field config. |
| Create meta | `GET /issue/createmeta` | Partial | projects + types. |
| Writes | `POST /issue`, `PUT /issue/{key}`, `POST …/comment`, `PUT …/assignee`, `POST …/attachments` | Supported | Mutate the in-memory graph. |
| Attachments | `GET /attachment/{id}`, `GET /attachment/content/{id}` | Supported | 302 to `/file/{uuid}/binary?name=`; the target serves the stored bytes. |
| Spaces | `GET /wiki/rest/api/space`, `GET /wiki/rest/api/space/{key}` | Supported | |
| CQL | `GET /wiki/rest/api/content/search` | Supported | `space`, `type`, `lastModified`; `_links.next`. |
| Page | `GET /wiki/rest/api/content/{id}` | Supported | `body.atlas_doc_format`. |
| Page versions | `GET /wiki/rest/api/content/{id}/version` | Supported | Newest-first (`number` desc). `start`/`limit`; `_links.next` uses the same `next=true&cursor=` convention as content/search. Each row has `by`, `when`, `message`, `number`, `minorEdit`. |
| Page comments | `GET /wiki/rest/api/content/{id}/child/comment` | Supported | |
| Wiki writes | `POST /wiki/rest/api/content`, `PUT /wiki/rest/api/content/{id}` | Supported | Create is version 1. Update requires `version.number == current+1` (409 otherwise). `version.message` is stored and served by `/version`. |

JQL subset: `project`, `key`, `updated`, `created`, `status`,
`statusCategory`, `issuetype`/`type`, `priority`, `assignee`, `reporter`,
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
expression eval, Confluence `/wiki/api/v2/pages`, `/wiki/rest/api/user/current`.

## Lab API

`/healthz`, `/api/overview`, `/api/requests`, `/api/data`, `/api/diff`,
`/api/compatibility`, `/api/diagnostics`, `/api/fixtures/apply`,
`/api/fixtures/snapshot`, `/api/scenarios/run`.

Every response includes `X-Issuetap: 1`.
