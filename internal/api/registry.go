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
		{Method: "GET", Path: "/rest/api/{v}/field", Level: Supported, Notes: "catalog names localize", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/search", Level: Supported, Notes: "values/isLast/total/startAt", Cloud: true, DC: false},
		{Method: "GET", Path: "/rest/api/{v}/project", Level: Supported, Notes: "DC project list (array)", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/project/{key}", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/search/jql", Level: Supported, Notes: "nextPageToken/isLast; Cloud only", Cloud: true, DC: false},
		{Method: "POST", Path: "/rest/api/{v}/search/approximate-count", Level: Supported, Cloud: true, DC: false},
		{Method: "POST", Path: "/rest/api/{v}/search", Level: Supported, Notes: "DC startAt/maxResults/total; Cloud legacy also served", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/search", Level: Partial, Notes: "same evaluator as POST; Cloud GET /search/jql is 400 on the real site", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}", Level: Supported, Notes: "expand=changelog", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/changelog", Level: Supported, Notes: "values/total/isLast", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/comment", Level: Supported, Notes: "startAt/maxResults/total", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/transitions", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/{key}/editmeta", Level: Partial, Notes: "summary/labels/priority/assignee", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/issue/createmeta", Level: Partial, Notes: "projects + issue types", Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/filter/my", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/user/search", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/attachment/{id}", Level: Supported, Cloud: true, DC: true},
		{Method: "GET", Path: "/rest/api/{v}/attachment/content/{id}", Level: Supported, Notes: "302 to /file/{uuid}/binary?name=", Cloud: true, DC: true},

		// Cloud + DC Jira writes (gadak write.go)
		{Method: "POST", Path: "/rest/api/{v}/issue", Level: Supported, Cloud: true, DC: true},
		{Method: "PUT", Path: "/rest/api/{v}/issue/{key}", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/comment", Level: Supported, Cloud: true, DC: true},
		{Method: "PUT", Path: "/rest/api/{v}/issue/{key}/assignee", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/transitions", Level: Supported, Cloud: true, DC: true},
		{Method: "POST", Path: "/rest/api/{v}/issue/{key}/attachments", Level: Supported, Notes: "requires X-Atlassian-Token: no-check", Cloud: true, DC: true},

		// Confluence Cloud (gadak confluence.Client)
		{Method: "GET", Path: "/wiki/rest/api/space", Level: Supported, Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/space/{key}", Level: Supported, Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/search", Level: Supported, Notes: "CQL; _links.next cursor", Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/{id}", Level: Supported, Notes: "expand=body.atlas_doc_format,version,space,ancestors,metadata.labels", Cloud: true, DC: false},
		{Method: "GET", Path: "/wiki/rest/api/content/{id}/child/comment", Level: Supported, Cloud: true, DC: false},

		// Confluence DC (read path under context)
		{Method: "GET", Path: "/rest/api/space", Level: Partial, Notes: "DC context path; body.storage", Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/space/{key}", Level: Partial, Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/search", Level: Partial, Cloud: false, DC: true},
		{Method: "GET", Path: "/rest/api/content/{id}", Level: Partial, Cloud: false, DC: true},
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
