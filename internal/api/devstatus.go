package api

// The development-panel read surface, in the shape Jira Cloud's internal
// /rest/dev-status API serves it (gadak GDK-497). Cloud never documented a
// public read for the panel; its own workaround (JSWCLOUD-16901) points
// clients at these two GETs, so a client written against Cloud works against
// issuetap unchanged. Payload shapes were captured live from a Cloud site on
// 2026-08-21 (empty panel) plus Atlassian's published detail example.
//
// The POST is issuetap's own: Cloud only lets a Connect/Forge app write the
// panel, but a standalone origin must accept links from its CLI.

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

// getDevSummary mirrors GET /rest/dev-status/latest/issue/summary. Only the
// pullrequest block carries live numbers; the other blocks exist because the
// captured Cloud payload carries them, zero-valued.
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
			"build":                  map[string]any{"overall": zero("build"), "byInstanceType": map[string]any{}},
			"review":                 map[string]any{"overall": zero("review"), "byInstanceType": map[string]any{}},
			"deployment-environment": map[string]any{"overall": zero("deployment-environment"), "byInstanceType": map[string]any{}},
			"repository":             map[string]any{"overall": zero("repository"), "byInstanceType": map[string]any{}},
			"branch":                 map[string]any{"overall": zero("branch"), "byInstanceType": map[string]any{}},
		},
	})
}

// getDevDetail mirrors GET /rest/dev-status/1.0/issue/detail. applicationType
// is required with Cloud's exact failure shape; its value is not matched —
// issuetap is the only instance there is.
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

// postDevLink accepts one pull-request link and upserts it by URL.
func (s *Server) postDevLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IssueID string `json:"issueId"`
		URL     string `json:"url"`
		Name    string `json:"name"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IssueID == "" || body.URL == "" {
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
	pr, err := s.st.LinkDevPR(body.IssueID, model.DevPR{URL: body.URL, Name: body.Name, Status: status})
	if err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, "Issue does not exist")
			return
		}
		// A persist failure kept the link in memory but not on disk; report
		// it as a 500 so the caller retries, instead of masking it as a
		// missing issue (GDK-537 audit).
		writeJiraWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pr)
}
