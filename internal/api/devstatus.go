package api

// The development-panel read surface, in the shape Jira Cloud's internal
// /rest/dev-status API serves it (gadak GDK-497). Cloud never documented a
// public read for the panel; its own workaround (JSWCLOUD-16901) points
// clients at these two GETs, so a client written against Cloud works against
// issuetap unchanged. Payload shapes were captured live from a Cloud site on
// 2026-08-21 (empty panel) plus Atlassian's published detail example. The
// build and deployment-environment summary blocks gained their extra keys
// (failedBuildCount / successfulBuildCount / unknownBuildCount; topEnvironments /
// showProjects / successfulCount) from live Cloud payloads pasted verbatim by
// users in Atlassian's tracker and community threads (checked 2026-08-22) —
// key spellings verbatim, values empty/false as captured. The detail row
// vocabulary for those two kinds was never captured anywhere: detail serves
// no rows for them, and no field is invented to fill the gap (GDK-592).
//
// The POST is issuetap's own: Cloud only lets a Connect/Forge app write the
// panel, but a standalone origin must accept links from its CLI. Its body
// carries a kind — pullrequest (default, byte-identical to the kindless
// body), deployment (environment + state), or build (state + optional
// number) — with the same URL-keyed upsert and actor stamping for each.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/midagedev/issuetap/internal/model"
	"github.com/midagedev/issuetap/internal/store"
)

func (s *Server) handleDevStatus(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, "/rest/dev-status/")
	// Cloud serves the same handlers under /latest/ and /1.0/.
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[i+1:]
	}
	switch {
	case r.Method == http.MethodGet && rest == "issue/summary":
		s.getDevSummary(w, r)
	case r.Method == http.MethodGet && rest == "issue/detail":
		s.getDevDetail(w, r)
	case r.Method == http.MethodPost && rest == "issue/link":
		s.postDevLink(w, r)
	default:
		writeUnsupported(w, r.Method, r.URL.Path)
	}
}

func (s *Server) devIssue(w http.ResponseWriter, r *http.Request) *model.Issue {
	id := r.URL.Query().Get("issueId")
	if id == "" {
		// Parity: Cloud answers 500 {"message":"<param>"} for a missing
		// required parameter on this internal API (measured 2026-08-21 with
		// applicationType; issueId gets the same treatment).
		writeDevParamError(w, "issueId")
		return nil
	}
	iss := s.st.Issue(id)
	if iss == nil {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return nil
	}
	return iss
}

func writeDevParamError(w http.ResponseWriter, param string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"message": param, "status-code": 500, "stack-trace": "",
	})
}

// getDevSummary mirrors GET /rest/dev-status/latest/issue/summary. The
// pullrequest, build, and deployment-environment blocks carry live numbers
// (the kinds the POST writes); the remaining blocks exist because the
// captured Cloud payload carries them, zero-valued. Build and deployment
// fill only captured keys — topEnvironments / showProjects stay at the
// captured empty values.
func (s *Server) getDevSummary(w http.ResponseWriter, r *http.Request) {
	iss := s.devIssue(w, r)
	if iss == nil {
		return
	}
	count := len(iss.DevPRs)
	state, last := "OPEN", any(nil)
	openCount := 0
	for _, pr := range iss.DevPRs {
		if pr.Status == "OPEN" {
			openCount++
		}
		if lastStr, ok := last.(string); !ok || pr.Updated > lastStr {
			last = pr.Updated
		}
	}
	if count > 0 && openCount == 0 {
		state = "MERGED"
	}
	bCount, bLast, bSucc, bFail, bUnknown := len(iss.DevBuilds), any(nil), 0, 0, 0
	for _, b := range iss.DevBuilds {
		switch b.State {
		case "successful":
			bSucc++
		case "failed":
			bFail++
		default:
			bUnknown++
		}
		if lastStr, ok := bLast.(string); !ok || b.Updated > lastStr {
			bLast = b.Updated
		}
	}
	dCount, dLast, dSucc := len(iss.DevDeployments), any(nil), 0
	for _, dep := range iss.DevDeployments {
		if dep.State == "successful" {
			dSucc++
		}
		if lastStr, ok := dLast.(string); !ok || dep.Updated > lastStr {
			dLast = dep.Updated
		}
	}
	zero := func(dataType string) map[string]any {
		return map[string]any{"count": 0, "lastUpdated": nil, "dataType": dataType}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"errors":       []any{},
		"configErrors": []any{},
		"summary": map[string]any{
			"pullrequest": map[string]any{
				"overall": map[string]any{
					"count": count, "lastUpdated": last, "stateCount": openCount,
					"state": state, "dataType": "pullrequest", "open": openCount > 0,
				},
				"byInstanceType": map[string]any{},
			},
			"build": map[string]any{
				"overall": map[string]any{
					"count": bCount, "lastUpdated": bLast,
					"successfulBuildCount": bSucc, "failedBuildCount": bFail,
					"unknownBuildCount": bUnknown, "dataType": "build",
				},
				"byInstanceType": map[string]any{},
			},
			"review":                 map[string]any{"overall": zero("review"), "byInstanceType": map[string]any{}},
			"deployment-environment": map[string]any{"overall": map[string]any{"count": dCount, "lastUpdated": dLast, "topEnvironments": []any{}, "showProjects": false, "successfulCount": dSucc, "dataType": "deployment-environment"}, "byInstanceType": map[string]any{}},
			"repository":             map[string]any{"overall": zero("repository"), "byInstanceType": map[string]any{}},
			"branch":                 map[string]any{"overall": zero("branch"), "byInstanceType": map[string]any{}},
		},
	})
}

// getDevDetail mirrors GET /rest/dev-status/1.0/issue/detail. applicationType
// is required with Cloud's exact failure shape; its value is not matched —
// issuetap is the only instance there is. Only pull requests are served:
// Cloud's detail row vocabulary for builds and deployments was never
// captured (the only keys ever observed in a detail entry are branches,
// pullRequests, repositories), so those dataTypes answer with an empty
// detail array rather than invented rows — their records are counted by
// the summary (GDK-592).
func (s *Server) getDevDetail(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("applicationType") == "" {
		writeDevParamError(w, "applicationType")
		return
	}
	if dt := r.URL.Query().Get("dataType"); dt == "" {
		writeDevParamError(w, "dataType")
		return
	}
	iss := s.devIssue(w, r)
	if iss == nil {
		return
	}
	detail := []any{}
	if r.URL.Query().Get("dataType") == "pullrequest" && len(iss.DevPRs) > 0 {
		prs := make([]any, 0, len(iss.DevPRs))
		for _, pr := range iss.DevPRs {
			prs = append(prs, pr)
		}
		detail = append(detail, map[string]any{
			"pullRequests": prs,
			"_instance": map[string]any{
				"name": "issuetap", "type": "GitHub",
				"id": "com.issuetap.devinfo", "singleInstance": true,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"errors": []any{}, "detail": detail})
}

// postDevLink accepts one dev link and upserts it. kind selects the row:
// "" or pullrequest (the original body, byte-identical — url required,
// status OPEN|MERGED|DECLINED, optional author and branch per GDK-589),
// deployment (environment + state required, url optional — a url-less row
// is keyed by its environment), or build (state required from the closed
// set successful|failed|unknown — the summary's three buckets — plus url
// or number for the key). The actor — the identity that wrote the link —
// is stamped here from the request identity and is never read from the
// body, so a client cannot forge it (all kinds).
func (s *Server) postDevLink(w http.ResponseWriter, r *http.Request) {
	var body devLinkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IssueID == "" {
		writeJiraError(w, http.StatusBadRequest, "issueId and url are required")
		return
	}
	switch strings.ToLower(strings.TrimSpace(body.Kind)) {
	case "", "pullrequest":
		s.postDevPRLink(w, r, body)
	case "deployment":
		s.postDevDeploymentLink(w, r, body)
	case "build":
		s.postDevBuildLink(w, r, body)
	default:
		writeJiraError(w, http.StatusBadRequest, "kind must be pullrequest, deployment or build")
	}
}

// devLinkBody is the POST /rest/dev-status/{v}/issue/link body. The
// pullrequest fields are the original set (GDK-497/589); kind and the
// per-kind fields extend it without touching the original path.
type devLinkBody struct {
	IssueID     string `json:"issueId"`
	URL         string `json:"url"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Author      string `json:"author"`
	Branch      string `json:"branch"`
	Kind        string `json:"kind"`
	Environment string `json:"environment"`
	State       string `json:"state"`
	Number      string `json:"number"`
}

// postDevPRLink is the kindless body's path, unchanged from GDK-497/589.
func (s *Server) postDevPRLink(w http.ResponseWriter, r *http.Request, body devLinkBody) {
	if body.URL == "" {
		writeJiraError(w, http.StatusBadRequest, "issueId and url are required")
		return
	}
	status := strings.ToUpper(strings.TrimSpace(body.Status))
	switch status {
	case "", "OPEN", "MERGED", "DECLINED":
	default:
		writeJiraError(w, http.StatusBadRequest, "status must be OPEN, MERGED or DECLINED")
		return
	}
	link := model.DevPR{URL: body.URL, Name: body.Name, Status: status}
	if body.Author != "" {
		link.Author = &model.DevAuthor{Name: body.Author}
	}
	if body.Branch != "" {
		link.Source = &model.DevSource{Branch: body.Branch}
	}
	if u := s.identity(r); u != nil {
		link.Actor = &model.DevActor{AccountID: u.AccountID, DisplayName: u.DisplayName}
	}
	pr, err := s.st.LinkDevPR(body.IssueID, link)
	if err != nil {
		s.writeDevLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pr)
}

// postDevDeploymentLink handles kind=deployment.
func (s *Server) postDevDeploymentLink(w http.ResponseWriter, r *http.Request, body devLinkBody) {
	environment := strings.TrimSpace(body.Environment)
	state := strings.ToLower(strings.TrimSpace(body.State))
	if environment == "" || state == "" {
		writeJiraError(w, http.StatusBadRequest, "environment and state are required")
		return
	}
	dep := model.DevDeployment{URL: strings.TrimSpace(body.URL), Environment: environment, State: state}
	if u := s.identity(r); u != nil {
		dep.Actor = &model.DevActor{AccountID: u.AccountID, DisplayName: u.DisplayName}
	}
	stored, err := s.st.LinkDevDeployment(body.IssueID, dep)
	if err != nil {
		s.writeDevLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// postDevBuildLink handles kind=build.
func (s *Server) postDevBuildLink(w http.ResponseWriter, r *http.Request, body devLinkBody) {
	state := strings.ToLower(strings.TrimSpace(body.State))
	if state == "" {
		writeJiraError(w, http.StatusBadRequest, "state is required")
		return
	}
	switch state {
	case "successful", "failed", "unknown":
	default:
		writeJiraError(w, http.StatusBadRequest, "state must be successful, failed or unknown")
		return
	}
	url := strings.TrimSpace(body.URL)
	number := strings.TrimSpace(body.Number)
	if url == "" && number == "" {
		writeJiraError(w, http.StatusBadRequest, "url or number is required")
		return
	}
	b := model.DevBuild{URL: url, Number: number, State: state}
	if u := s.identity(r); u != nil {
		b.Actor = &model.DevActor{AccountID: u.AccountID, DisplayName: u.DisplayName}
	}
	stored, err := s.st.LinkDevBuild(body.IssueID, b)
	if err != nil {
		s.writeDevLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, stored)
}

// writeDevLinkError maps a store error from any dev-link write: 404 for a
// missing issue, 500 for a persist failure so the caller retries (GDK-537).
func (s *Server) writeDevLinkError(w http.ResponseWriter, err error) {
	if store.IsNotFound(err) {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return
	}
	writeJiraWriteError(w, err)
}
