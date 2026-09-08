package api

// Jira Software Agile 1.0 handlers (gadak GDK-1666, docs/decisions/0002).
// This is API compatibility, not a planning feature: the surface is what
// gadak's first-class sprints talk to — boards (one scrum board per
// project, lazily created), sprint CRUD with the two transitions Jira's
// own API defines, backlog moves, and nothing else. Anything else under
// the prefix stays an honest unsupported_endpoint.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/model"
	"github.com/midagedev/issuetap/internal/store"
)

const agilePrefix = "/rest/agile/1.0"

// sprintIssueCap is Jira's own per-call cap on the move endpoints.
const sprintIssueCap = 50

func (s *Server) handleAgile(w http.ResponseWriter, r *http.Request, path string) {
	segs := strings.Split(strings.Trim(strings.TrimPrefix(path, agilePrefix), "/"), "/")
	switch {
	case len(segs) == 1 && segs[0] == "board" && r.Method == http.MethodGet:
		s.getAgileBoards(w, r)
	case len(segs) == 3 && segs[0] == "board" && segs[2] == "sprint" && r.Method == http.MethodGet:
		s.getAgileBoardSprints(w, r, segs[1])
	case len(segs) == 1 && segs[0] == "sprint" && r.Method == http.MethodPost:
		s.createAgileSprint(w, r)
	case len(segs) == 2 && segs[0] == "sprint" && r.Method == http.MethodGet:
		s.getAgileSprint(w, r, segs[1])
	case len(segs) == 2 && segs[0] == "sprint" && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		s.updateAgileSprint(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "sprint" && segs[2] == "issue" && r.Method == http.MethodGet:
		s.getAgileSprintIssues(w, r, segs[1])
	case len(segs) == 3 && segs[0] == "sprint" && segs[2] == "issue" && r.Method == http.MethodPost:
		s.moveAgileIssues(w, r, segs[1])
	case len(segs) == 2 && segs[0] == "backlog" && segs[1] == "issue" && r.Method == http.MethodPost:
		s.moveAgileIssues(w, r, "backlog")
	default:
		writeUnsupported(w, r.Method, path)
	}
}

// agileID parses a path id; a non-numeric or non-positive one is the same
// 404 as an unknown one.
func agileID(w http.ResponseWriter, kind, seg string) (int64, bool) {
	id, err := strconv.ParseInt(seg, 10, 64)
	if err != nil || id <= 0 {
		writeAgileNotFound(w, kind, seg)
		return 0, false
	}
	return id, true
}

// writeAgileNotFound is the Agile 404: Jira Software phrases missing
// boards and sprints as permission-or-existence, never a bare 404 body.
func writeAgileNotFound(w http.ResponseWriter, kind, id string) {
	noun := map[string]string{"board": "Board", "sprint": "Sprint"}[kind]
	if noun == "" {
		noun = "Resource"
	}
	writeJSON(w, http.StatusNotFound, jiraError{
		ErrorMessages: []string{noun + " does not exist or you do not have permission to view it."},
		Errors:        map[string]string{},
	})
}

// writeAgileStoreError maps store errors onto the Agile envelope:
// not-found keeps Jira Software's permission-or-existence phrasing,
// everything write-shaped (bad transition, closed sprint, persist) is
// Jira's 400/500 split, same as writeJiraWriteError.
func writeAgileStoreError(w http.ResponseWriter, err error) {
	if store.IsNotFound(err) {
		writeAgileNotFound(w, store.NotFoundKind(err), store.NotFoundID(err))
		return
	}
	writeJiraWriteError(w, err)
}

func pagedSlice[T any](in []T, startAt, maxResults int) []T {
	if startAt < 0 {
		startAt = 0
	}
	if startAt > len(in) {
		return nil
	}
	end := len(in)
	if maxResults > 0 && startAt+maxResults < end {
		end = startAt + maxResults
	}
	return in[startAt:end]
}

// getAgileBoards is GET /rest/agile/1.0/board. Materializes the boards
// (store-side, project-key order) before filtering, so ids are stable
// regardless of which filter ran first.
func (s *Server) getAgileBoards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	all := s.st.Boards(q.Get("projectKeyOrId"), q.Get("type"))
	startAt, maxResults := atoiDefault(q.Get("startAt"), 0), atoiDefault(q.Get("maxResults"), 50)
	values := pagedSlice(all, startAt, maxResults)
	out := make([]any, 0, len(values))
	for _, b := range values {
		out = append(out, s.boardJSON(r, b))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"maxResults": maxResults, "startAt": startAt, "total": len(all),
		"isLast": startAt+len(values) >= len(all), "values": out,
	})
}

func (s *Server) boardJSON(r *http.Request, b model.Board) map[string]any {
	loc := map[string]any{"projectKey": b.ProjectKey}
	if p := s.st.Project(b.ProjectKey); p != nil {
		loc["projectId"] = atoiDefault(p.ID, 0)
		loc["name"] = p.Name
	}
	return map[string]any{
		"id": b.ID, "self": s.selfURL(r, agilePrefix+"/board/"+strconv.FormatInt(b.ID, 10)),
		"name": b.Name, "type": b.Type, "location": loc,
	}
}

// sprintJSON is the Agile route shape: state lowercase, goal always
// present, absent dates omitted, originBoardId (not boardId).
func (s *Server) sprintJSON(r *http.Request, sp model.Sprint) map[string]any {
	out := map[string]any{
		"id": sp.ID, "self": s.selfURL(r, agilePrefix+"/sprint/"+strconv.FormatInt(sp.ID, 10)),
		"state": sp.State, "name": sp.Name, "goal": sp.Goal,
		"originBoardId": sp.BoardID,
	}
	for k, v := range map[string]string{
		"startDate": sp.StartDate, "endDate": sp.EndDate,
		"completeDate": sp.CompleteDate, "activatedDate": sp.ActivatedDate,
	} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func (s *Server) getAgileBoardSprints(w http.ResponseWriter, r *http.Request, seg string) {
	id, ok := agileID(w, "board", seg)
	if !ok {
		return
	}
	var states []string
	if raw := r.URL.Query().Get("state"); raw != "" {
		for _, st := range strings.Split(raw, ",") {
			st = strings.TrimSpace(st)
			switch st {
			case model.SprintFuture, model.SprintActive, model.SprintClosed:
				states = append(states, st)
			default:
				writeJiraError(w, http.StatusBadRequest, "Invalid state parameter: "+st)
				return
			}
		}
	}
	all, err := s.st.BoardSprints(id, states)
	if err != nil {
		writeAgileStoreError(w, err)
		return
	}
	startAt, maxResults := atoiDefault(r.URL.Query().Get("startAt"), 0), atoiDefault(r.URL.Query().Get("maxResults"), 50)
	values := pagedSlice(all, startAt, maxResults)
	out := make([]any, 0, len(values))
	for _, sp := range values {
		out = append(out, s.sprintJSON(r, sp))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"maxResults": maxResults, "startAt": startAt, "total": len(all),
		"isLast": startAt+len(values) >= len(all), "values": out,
	})
}

func (s *Server) getAgileSprint(w http.ResponseWriter, r *http.Request, seg string) {
	id, ok := agileID(w, "sprint", seg)
	if !ok {
		return
	}
	sp, err := s.st.Sprint(id)
	if err != nil {
		writeAgileStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sprintJSON(r, *sp))
}

// agileSprintBody is the create/update body. Pointers carry presence: a
// key the request did not send is not applied, distinct from sent-empty.
type agileSprintBody struct {
	Name          *string `json:"name"`
	OriginBoardID *int64  `json:"originBoardId"`
	Goal          *string `json:"goal"`
	State         *string `json:"state"`
	StartDate     *string `json:"startDate"`
	EndDate       *string `json:"endDate"`
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Server) createAgileSprint(w http.ResponseWriter, r *http.Request) {
	var body agileSprintBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	errs := map[string]string{}
	if body.Name == nil || strings.TrimSpace(*body.Name) == "" {
		errs["name"] = "Sprint name must be specified."
	}
	if body.OriginBoardID == nil {
		errs["originBoardId"] = "Origin board id must be specified."
	}
	if len(errs) > 0 {
		writeJiraFieldErrors(w, errs)
		return
	}
	sp, err := s.st.CreateSprint(*body.OriginBoardID, strings.TrimSpace(*body.Name),
		derefStr(body.Goal), derefStr(body.StartDate), derefStr(body.EndDate))
	if err != nil {
		writeAgileStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.sprintJSON(r, *sp))
}

func (s *Server) updateAgileSprint(w http.ResponseWriter, r *http.Request, seg string) {
	id, ok := agileID(w, "sprint", seg)
	if !ok {
		return
	}
	var body agileSprintBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if body.State != nil {
		switch *body.State {
		case model.SprintFuture, model.SprintActive, model.SprintClosed:
		default:
			writeJiraError(w, http.StatusBadRequest, "Invalid sprint state: "+*body.State)
			return
		}
	}
	patch := store.SprintPatch{
		Name: body.Name, Goal: body.Goal, State: body.State,
		StartDate: body.StartDate, EndDate: body.EndDate,
	}
	sp, err := s.st.UpdateSprint(id, patch, s.identity(r).AccountID)
	if err != nil {
		writeAgileStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.sprintJSON(r, *sp))
}

func (s *Server) getAgileSprintIssues(w http.ResponseWriter, r *http.Request, seg string) {
	id, ok := agileID(w, "sprint", seg)
	if !ok {
		return
	}
	issues, err := s.st.SprintIssues(id)
	if err != nil {
		writeAgileStoreError(w, err)
		return
	}
	out := make([]any, 0, len(issues))
	for _, iss := range issues {
		out = append(out, s.issueJSON(r, iss, nil, ""))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": out})
}

// moveAgileIssues is POST /sprint/{id}/issue and POST /backlog/issue —
// the same body, the same cap, the same unknown-key 404; seg "backlog"
// names the latter.
func (s *Server) moveAgileIssues(w http.ResponseWriter, r *http.Request, seg string) {
	var body struct {
		Issues []string `json:"issues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(body.Issues) == 0 {
		writeJiraError(w, http.StatusBadRequest, "Issues must be specified.")
		return
	}
	if len(body.Issues) > sprintIssueCap {
		writeJiraError(w, http.StatusBadRequest,
			"A maximum of "+strconv.Itoa(sprintIssueCap)+" issues can be moved at once.")
		return
	}
	var err error
	if seg == "backlog" {
		err = s.st.MoveIssuesToBacklog(body.Issues, s.identity(r).AccountID)
	} else {
		var id int64
		if id, err = strconv.ParseInt(seg, 10, 64); err == nil && id > 0 {
			err = s.st.MoveIssuesToSprint(id, body.Issues, s.identity(r).AccountID)
		} else {
			writeAgileNotFound(w, "sprint", seg)
			return
		}
	}
	if err != nil {
		// An unknown issue key is the per-key 404: the errors map names
		// the key, the message explains it generically.
		if store.IsNotFound(err) && store.NotFoundKind(err) == "issue" {
			msg := "Issue does not exist or you do not have permission to view it."
			writeJSON(w, http.StatusNotFound, jiraError{
				ErrorMessages: []string{msg},
				Errors:        map[string]string{store.NotFoundID(err): msg},
			})
			return
		}
		writeAgileStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
