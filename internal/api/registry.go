package api

import "strings"

// Level is a compatibility claim.
type Level string

const (
	Supported   Level = "supported"
	Partial     Level = "partial"
	Unsupported Level = "unsupported"
	IssuetapAPI Level = "issuetap"
)

// Route is one row of the compatibility table. COMPATIBILITY.md is generated
// from this list so the document cannot drift from the router.
type Route struct {
	Method string
	Path   string // pattern, with {param}
	Level  Level
	Notes  string
	Cloud  bool
	DC     bool
}

// Inventory is the v0 surface. Known-but-unimplemented Atlassian routes
// stay on this list at Unsupported so they return unsupported_endpoint
// instead of a 404.
func Inventory() []Route {
	return []Route{
		// Cloud + DC Jira reads (gadak call set)
		{Method: "GET", Path: "/rest/api/{v}/myself", Level: Supported, Notes: "credential probe", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/serverInfo", Level: Supported, Notes: "deploymentType Cloud|DataCenter; serverTitle is issuetap", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/status", Level: Supported, Notes: "localized names; statusCategory.key stable", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/priority", Level: Supported, Notes: "most-urgent first; names localize under --locale", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issuetype", Level: Supported, Notes: "hierarchyLevel present", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/resolution", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issueLinkType", Level: Supported, Notes: "Cloud default 4: Blocks, Cloners, Duplicate, Relates (ids 10000–10003)", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/field", Level: Supported, Notes: "catalog names localize", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/search", Level: Supported, Notes: "values/isLast/total/startAt", Cloud: true, DC: false},
		{Method: "GET", Path: "/rest/api/{v}/project", Level: Supported, Notes: "DC project list (array)", Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/project", Level: Supported, Notes: "key and name; duplicate or invalid key 400", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/{key}", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/{key}/versions", Level: Supported, Notes: "issue-derived {id,name} catalog; released/archived always false; no releaseDate", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/{key}/components", Level: Supported, Notes: "issue-derived {id,name} catalog; same shape as /versions", Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/search/jql", Level: Supported, Notes: "nextPageToken/isLast; Cloud only", Cloud: true, DC: false},
		{Method: "POST", Path: "/rest/api/{v}/search/approximate-count", Level: Supported, Cloud: true, DC: false},
		{Method: "POST", Path: "/rest/api/{v}/search", Level: Supported, Notes: "DC startAt/maxResults/total; Cloud legacy also served", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/search", Level: Partial, Notes: "same evaluator as POST; Cloud GET /search/jql is 400 on the real site", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}", Level: Supported, Notes: "expand=changelog", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/changelog", Level: Supported, Notes: "values/total/isLast", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/comment", Level: Supported, Notes: "startAt/maxResults/total; echoes stored visibility and jsdPublic (keys omitted when unset)", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/transitions", Level: Supported, Notes: "expand=transitions.fields includes fields ({} when the destination has no screen)", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/editmeta", Level: Partial, Notes: "summary/description/labels/priority/assignee/duedate/parent/issuetype/fixVersions/components + fixture custom fields; option and named-list allowedValues", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/createmeta", Level: Partial, Notes: "projects + issue types", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/createmeta/{projectIdOrKey}/issuetypes/{id}", Level: Partial, Notes: "fields list + startAt/maxResults/total; required/hasDefaultValue from CreateIssue; fixVersions/components optional; not per-screen", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/filter/my", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/user/search", Level: Supported, Notes: "query=me is the /myself identity", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/attachment/{id}", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/attachment/content/{id}", Level: Supported, Notes: "302 to /file/{uuid}/binary?name=, which serves the stored bytes", Cloud: true, DC: true},

		// Cloud + DC Jira writes (gadak write.go)
		{Method: "POST", Path: "/rest/api/{v}/issue", Level: Supported, Cloud: true, DC: true},
		{Method: "PUT", Path: "/rest/api/{v}/issue/{key}", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/comment", Level: Supported, Notes: "stores visibility {type:role|group,value} and sd.public.comment as jsdPublic; invalid type 400 errors.visibility", Cloud: true, DC: true},
		{Method: "PUT", Path: "/rest/api/{v}/issue/{key}/assignee", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/transitions", Level: Supported, Notes: "stores fields.resolution by catalog id; undeclared screen fields 400; done without a resolution defaults to 10000", Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/claim", Level: Supported, Notes: "issuetap extension (no Atlassian route): atomic assignee + in-progress transition under one lock; claimed by another actor is 409 naming the holder; same actor is idempotent; takeOver overrides; no in-progress destination is 400; claimedAt is read from the changelog", Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/attachments", Level: Supported, Notes: "requires X-Atlassian-Token: no-check", Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issueLink", Level: Supported, Notes: "stores the pair on both issues; unknown type 404; missing issue 404; self-link 400; duplicate same type/pair/direction is idempotent 201", Cloud: true, DC: true},
		{Method: "DELETE", Path: "/rest/api/{v}/issueLink/{id}", Level: Supported, Notes: "id is the synthetic typeID:outwardEnd:inwardEnd this server's GET emits; removes both projections; unknown or already-removed id 404", Cloud: true, DC: true},

		// Cloud development panel (gadak GDK-497)
		{Method: "GET", Path: "/rest/dev-status/{v}/issue/summary", Level: Supported, Notes: "pullrequest counts; other blocks zero-valued", Cloud: true, DC: false},
		{Method: "GET", Path: "/rest/dev-status/{v}/issue/detail", Level: Supported, Notes: "applicationType and dataType required; Cloud 500 param shape", Cloud: true, DC: false},
		{Method: "POST", Path: "/rest/dev-status/{v}/issue/link", Level: Supported, Notes: "upserts one pull-request link by URL", Cloud: true, DC: false},

		// Confluence Cloud (gadak confluence.Client)
		{Method: "GET", Path: "/wiki/rest/api/space", Level: Supported, Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/space/{key}", Level: Supported, Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/search", Level: Supported, Notes: "CQL; _links.next cursor", Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/{id}", Level: Supported, Notes: "expand=body.atlas_doc_format,version,space,ancestors,metadata.labels", Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/{id}/version", Level: Supported, Notes: "newest-first (number desc); start/limit; _links.next cursor like content/search", Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/{id}/child/comment", Level: Supported, Cloud: true, DC: false},
		{Method: "POST", Path: "/wiki/rest/api/content", Level: Supported, Notes: "create page; body.atlas_doc_format.value is an ADF JSON string; version 1", Cloud: true, DC: false},
		{Method: "PUT", Path: "/wiki/rest/api/content/{id}", Level: Supported, Notes: "version.number must equal current+1 or 409; version.message is stored on the new history row", Cloud: true, DC: false},

		// Confluence DC (read path under context)
		{Method: "GET", Path: "/rest/api/space", Level: Partial, Notes: "DC context path; body.storage", Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/space/{key}", Level: Partial, Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/search", Level: Partial, Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/{id}", Level: Partial, Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/{id}/version", Level: Partial, Notes: "DC context path; same newest-first list as Cloud", Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/{id}/child/comment", Level: Partial, Cloud: false, DC: true},

		// Known unimplemented — must not 404
		{Method: "GET", Path: "/rest/api/{v}/dashboard", Level: Unsupported, Notes: "boards/dashboards are out of v0", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/board", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/agile/1.0/board", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/webhook", Level: Unsupported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/webhook", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/permissions", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/mypermissions", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/application-properties", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/group/member", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/jql/autocompletedata", Level: Unsupported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/expression/eval", Level: Unsupported, Cloud: true, DC: true},
		{Method: "GET", Path: "/wiki/rest/api/user/current", Level: Unsupported, Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/api/v2/pages", Level: Unsupported, Notes: "v2 Confluence API is out of v0", Cloud: true, DC: false},

		// issuetap surfaces
		{Method: "GET", Path: "/healthz", Level: IssuetapAPI, Notes: "liveness"},
		{Method: "GET", Path: "/api/overview", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/requests", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/data", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/diff", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/compatibility", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/diagnostics", Level: IssuetapAPI},
		{Method: "POST", Path: "/api/fixtures/apply", Level: IssuetapAPI},
		{Method: "GET", Path: "/api/fixtures/snapshot", Level: IssuetapAPI},
		{Method: "POST", Path: "/api/scenarios/run", Level: IssuetapAPI},
	}
}

// MatchUnsupported reports whether method+path is a known unimplemented
// Atlassian route. Path may include a context prefix; it is stripped by the
// caller before matching.
func MatchUnsupported(method, path string) bool {
	path = strings.TrimRight(path, "/")
	for _, r := range Inventory() {
		if r.Level != Unsupported {
			continue
		}
		if !strings.EqualFold(r.Method, method) {
			continue
		}
		if glob(r.Path, path) {
			return true
		}
		// /rest/api/{v}/... also matches the concrete /rest/api/2 or /3 form
		alt := strings.ReplaceAll(r.Path, "/{v}/", "/3/")
		if glob(alt, path) {
			return true
		}
		alt = strings.ReplaceAll(r.Path, "/{v}/", "/2/")
		if glob(alt, path) {
			return true
		}
	}
	return false
}

func glob(pattern, path string) bool {
	// Segment glob: {param} matches one path segment.
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(ps) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], "{") && strings.HasSuffix(pp[i], "}") {
			continue
		}
		if !strings.EqualFold(pp[i], ps[i]) {
			return false
		}
	}
	return true
}
