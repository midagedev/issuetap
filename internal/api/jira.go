package api

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
	"github.com/midagedev/issuetap/internal/store"
)

func (s *Server) handleJira(w http.ResponseWriter, r *http.Request, path string) {
	// Normalise /rest/api/3 and /rest/api/2 to a suffix after /rest/api/{v}
	suffix := path
	for _, p := range []string{"/rest/api/3", "/rest/api/2"} {
		if path == p {
			suffix = "/"
			break
		}
		if strings.HasPrefix(path, p+"/") {
			suffix = strings.TrimPrefix(path, p)
			break
		}
	}

	switch {
	case r.Method == http.MethodGet && suffix == "/myself":
		s.getMyself(w, r)
	case r.Method == http.MethodGet && suffix == "/serverInfo":
		s.getServerInfo(w, r)
	case r.Method == http.MethodGet && suffix == "/status":
		s.getStatuses(w, r)
	case r.Method == http.MethodGet && suffix == "/priority":
		s.getPriorities(w, r)
	case r.Method == http.MethodGet && suffix == "/issuetype":
		s.getIssueTypes(w, r)
	case r.Method == http.MethodGet && suffix == "/resolution":
		s.getResolutions(w, r)
	case r.Method == http.MethodGet && suffix == "/field":
		s.getFields(w, r)
	case r.Method == http.MethodGet && suffix == "/project/search":
		s.getProjectSearch(w, r)
	case r.Method == http.MethodGet && suffix == "/project":
		s.getProjects(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/project/"):
		s.getProject(w, r, strings.TrimPrefix(suffix, "/project/"))
	case r.Method == http.MethodPost && suffix == "/search/jql":
		s.postSearchJQL(w, r)
	case r.Method == http.MethodPost && suffix == "/search/approximate-count":
		s.postApproxCount(w, r)
	case (r.Method == http.MethodPost || r.Method == http.MethodGet) && suffix == "/search":
		s.legacySearch(w, r)
	case r.Method == http.MethodGet && suffix == "/filter/my":
		s.getMyFilters(w, r)
	case r.Method == http.MethodGet && suffix == "/user/search":
		s.getUserSearch(w, r)
	case r.Method == http.MethodGet && suffix == "/issue/createmeta":
		s.getCreateMeta(w, r)
	case r.Method == http.MethodPost && suffix == "/issue":
		s.postIssue(w, r)
	case strings.HasPrefix(suffix, "/issue/"):
		s.handleIssue(w, r, strings.TrimPrefix(suffix, "/issue/"))
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/attachment/content/"):
		s.getAttachmentContent(w, r, strings.TrimPrefix(suffix, "/attachment/content/"))
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/attachment/"):
		s.getAttachment(w, r, strings.TrimPrefix(suffix, "/attachment/"))
	default:
		if MatchUnsupported(r.Method, path) {
			writeUnsupported(w, r.Method, path)
			return
		}
		// Known Jira prefix but not implemented: still unsupported_endpoint,
		// never a silent 404.
		writeUnsupported(w, r.Method, path)
	}
}

func (s *Server) getMyself(w http.ResponseWriter, r *http.Request) {
	u := s.identity(r)
	out := s.userJSON(*u)
	out["self"] = s.selfURL(r, s.cfg.Dialect.JiraPrefix()+"/myself")
	out["expand"] = "groups,applicationRoles"
	out["groups"] = map[string]any{"size": 0, "items": []any{}}
	out["applicationRoles"] = map[string]any{"size": 0, "items": []any{}}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) identity(r *http.Request) *model.User {
	user := r.Header.Get("X-Issuetap-User")
	if user != "" {
		if u := s.st.UserByEmail(user); u != nil {
			return u
		}
		if u := s.st.User(user); u != nil {
			return u
		}
	}
	return s.st.DefaultUser()
}

func (s *Server) getServerInfo(w http.ResponseWriter, r *http.Request) {
	dep := "Cloud"
	if s.cfg.Dialect.Kind != "cloud" {
		dep = "DataCenter"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"baseUrl":        s.selfURL(r, ""),
		"version":        "1001.0.0-SNAPSHOT",
		"versionNumbers": []int{1001, 0, 0},
		"deploymentType": dep,
		"serverTitle":    "issuetap",
		"defaultLocale":  map[string]any{"locale": locale.BCP47(s.st.Locale())},
		"serverTimeZone": "Asia/Seoul",
		"scmInfo":        "issuetap",
		"buildNumber":    1,
	})
}

func (s *Server) getStatuses(w http.ResponseWriter, r *http.Request) {
	list := s.st.Statuses()
	out := make([]any, 0, len(list))
	for _, st := range list {
		out = append(out, s.statusJSON(st))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getPriorities(w http.ResponseWriter, r *http.Request) {
	list := s.st.Priorities()
	out := make([]any, 0, len(list))
	for _, p := range list {
		out = append(out, s.priorityJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getIssueTypes(w http.ResponseWriter, r *http.Request) {
	list := s.st.IssueTypes()
	out := make([]any, 0, len(list))
	for _, t := range list {
		out = append(out, s.typeJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getResolutions(w http.ResponseWriter, r *http.Request) {
	list := s.st.Resolutions()
	out := make([]any, 0, len(list))
	for _, rsl := range list {
		out = append(out, map[string]any{"id": rsl.ID, "name": rsl.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getFields(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.st.Fields())
}

func (s *Server) getProjectSearch(w http.ResponseWriter, r *http.Request) {
	start := atoiDefault(r.URL.Query().Get("startAt"), 0)
	max := atoiDefault(r.URL.Query().Get("maxResults"), 50)
	if max <= 0 {
		max = 50
	}
	all := s.st.Projects()
	total := len(all)
	if start > total {
		start = total
	}
	end := start + max
	if end > total {
		end = total
	}
	values := make([]any, 0, end-start)
	for _, p := range all[start:end] {
		values = append(values, s.projectJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"self":       s.selfURL(r, r.URL.Path),
		"startAt":    start,
		"maxResults": max,
		"total":      total,
		"isLast":     end >= total,
		"values":     values,
	})
}

func (s *Server) getProjects(w http.ResponseWriter, r *http.Request) {
	all := s.st.Projects()
	out := make([]any, 0, len(all))
	for _, p := range all {
		out = append(out, s.projectJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request, key string) {
	key = strings.Trim(key, "/")
	p := s.st.Project(key)
	if p == nil {
		writeJiraError(w, http.StatusNotFound, "No project could be found with key '"+key+"'.")
		return
	}
	writeJSON(w, http.StatusOK, s.projectJSON(p))
}

func (s *Server) projectJSON(p *model.Project) map[string]any {
	return map[string]any{
		"id":             p.ID,
		"key":            p.Key,
		"name":           p.Name,
		"projectTypeKey": p.TypeKey,
		"style":          p.Style,
		"simplified":     p.Simplified,
		"isPrivate":      false,
	}
}

type searchBody struct {
	JQL           string `json:"jql"`
	MaxResults    int    `json:"maxResults"`
	StartAt       int    `json:"startAt"`
	Fields        any    `json:"fields"`
	Expand        string `json:"expand"`
	NextPageToken string `json:"nextPageToken"`
}

func decodeFields(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return strings.Split(t, ",")
	}
	return nil
}

func (s *Server) postSearchJQL(w http.ResponseWriter, r *http.Request) {
	var body searchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	max := body.MaxResults
	if max <= 0 {
		max = 50
	}
	offset := 0
	if body.NextPageToken != "" {
		n, err := strconv.Atoi(body.NextPageToken)
		if err != nil || n < 0 {
			writeJiraError(w, http.StatusBadRequest, "Invalid nextPageToken")
			return
		}
		offset = n
	}
	issues, total, err := s.st.Search(body.JQL, offset, max)
	if err != nil {
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	fields := decodeFields(body.Fields)
	outIssues := make([]any, 0, len(issues))
	for _, iss := range issues {
		outIssues = append(outIssues, s.issueJSON(r, iss, fields, body.Expand))
	}
	page := map[string]any{"issues": outIssues}
	next := offset + len(issues)
	if next < total {
		page["nextPageToken"] = strconv.Itoa(next)
		page["isLast"] = false
	} else {
		page["isLast"] = true
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) postApproxCount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		JQL string `json:"jql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	n, err := s.st.Count(body.JQL)
	if err != nil {
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n})
}

func (s *Server) legacySearch(w http.ResponseWriter, r *http.Request) {
	var body searchBody
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeJiraError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
	} else {
		body.JQL = r.URL.Query().Get("jql")
		body.StartAt = atoiDefault(r.URL.Query().Get("startAt"), 0)
		body.MaxResults = atoiDefault(r.URL.Query().Get("maxResults"), 50)
		body.Expand = r.URL.Query().Get("expand")
		if f := r.URL.Query().Get("fields"); f != "" {
			body.Fields = f
		}
	}
	max := body.MaxResults
	if max <= 0 {
		max = 50
	}
	start := body.StartAt
	if start < 0 {
		start = 0
	}
	// DC startAt drift: an insert above the cursor skips a row. Cloud
	// nextPageToken is stable; this only applies when a fault sets drift.
	if driftOn(r) && start > 0 {
		start++
	}
	issues, total, err := s.st.Search(body.JQL, start, max)
	if err != nil {
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	fields := decodeFields(body.Fields)
	outIssues := make([]any, 0, len(issues))
	for _, iss := range issues {
		outIssues = append(outIssues, s.issueJSON(r, iss, fields, body.Expand))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"expand":     "schema,names",
		"startAt":    start,
		"maxResults": max,
		"total":      total,
		"issues":     outIssues,
	})
}

func (s *Server) getMyFilters(w http.ResponseWriter, r *http.Request) {
	list := s.st.Filters()
	out := make([]any, 0, len(list))
	for _, f := range list {
		item := map[string]any{
			"id": f.ID, "name": f.Name, "jql": f.JQL, "favourite": f.Favourite,
		}
		if f.Owner != "" {
			item["owner"] = map[string]any{"displayName": f.Owner}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getUserSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	max := atoiDefault(r.URL.Query().Get("maxResults"), 20)
	users := s.st.SearchUsers(q, max)
	out := make([]any, 0, len(users))
	for _, u := range users {
		out = append(out, s.userJSON(u))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getCreateMeta(w http.ResponseWriter, r *http.Request) {
	want := map[string]bool{}
	if keys := r.URL.Query().Get("projectKeys"); keys != "" {
		for _, k := range strings.Split(keys, ",") {
			want[strings.TrimSpace(k)] = true
		}
	}
	types := s.st.IssueTypes()
	typeArr := make([]any, 0, len(types))
	for _, t := range types {
		typeArr = append(typeArr, s.typeJSON(t))
	}
	var projects []any
	for _, p := range s.st.Projects() {
		if len(want) > 0 && !want[p.Key] {
			continue
		}
		projects = append(projects, map[string]any{
			"id": p.ID, "key": p.Key, "name": p.Name, "issuetypes": typeArr,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request, rest string) {
	// rest is {key} or {key}/comment etc.
	key, extra, _ := strings.Cut(rest, "/")
	key = strings.TrimSpace(key)
	if key == "" {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return
	}
	switch {
	case extra == "" && r.Method == http.MethodGet:
		s.getIssue(w, r, key)
	case extra == "" && r.Method == http.MethodPut:
		s.putIssue(w, r, key)
	case extra == "comment" && r.Method == http.MethodGet:
		s.getComments(w, r, key)
	case extra == "comment" && r.Method == http.MethodPost:
		s.postComment(w, r, key)
	case extra == "changelog" && r.Method == http.MethodGet:
		s.getChangelog(w, r, key)
	case extra == "transitions" && r.Method == http.MethodGet:
		s.getTransitions(w, r, key)
	case extra == "transitions" && r.Method == http.MethodPost:
		s.postTransition(w, r, key)
	case extra == "assignee" && r.Method == http.MethodPut:
		s.putAssignee(w, r, key)
	case extra == "editmeta" && r.Method == http.MethodGet:
		s.getEditMeta(w, r, key)
	case extra == "attachments" && r.Method == http.MethodPost:
		s.postAttachment(w, r, key)
	default:
		writeUnsupported(w, r.Method, r.URL.Path)
	}
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request, key string) {
	iss := s.st.Issue(key)
	if iss == nil {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist or you do not have permission to see it.")
		return
	}
	fields := decodeFields(r.URL.Query().Get("fields"))
	if q := r.URL.Query().Get("fields"); q != "" {
		fields = strings.Split(q, ",")
	}
	writeJSON(w, http.StatusOK, s.issueJSON(r, iss, fields, r.URL.Query().Get("expand")))
}

func (s *Server) putIssue(w http.ResponseWriter, r *http.Request, key string) {
	var body struct {
		Fields map[string]any `json:"fields"`
		Update map[string]any `json:"update"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := s.st.UpdateIssue(key, body.Fields, body.Update); err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, "Issue does not exist")
			return
		}
		if fe, ok := store.AsFieldError(err); ok {
			writeJiraFieldErrors(w, map[string]string{fe.Field: fe.Msg})
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getComments(w http.ResponseWriter, r *http.Request, key string) {
	iss := s.st.Issue(key)
	if iss == nil {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return
	}
	start := atoiDefault(r.URL.Query().Get("startAt"), 0)
	max := atoiDefault(r.URL.Query().Get("maxResults"), 100)
	if max <= 0 {
		max = 100
	}
	total := len(iss.Comments)
	if start > total {
		start = total
	}
	end := start + max
	if end > total {
		end = total
	}
	out := make([]any, 0, end-start)
	for _, c := range iss.Comments[start:end] {
		out = append(out, s.commentJSON(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"startAt": start, "maxResults": max, "total": total, "comments": out,
	})
}

func (s *Server) postComment(w http.ResponseWriter, r *http.Request, key string) {
	var body struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	cm, err := s.st.AddComment(key, s.identity(r).AccountID, body.Body)
	if err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, "Issue does not exist")
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.commentJSON(cm))
}

func (s *Server) getChangelog(w http.ResponseWriter, r *http.Request, key string) {
	iss := s.st.Issue(key)
	if iss == nil {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return
	}
	start := atoiDefault(r.URL.Query().Get("startAt"), 0)
	max := atoiDefault(r.URL.Query().Get("maxResults"), 100)
	writeJSON(w, http.StatusOK, s.changelogObj(iss, start, max))
}

func (s *Server) getTransitions(w http.ResponseWriter, r *http.Request, key string) {
	if s.st.Issue(key) == nil {
		writeJiraError(w, http.StatusNotFound, "Issue does not exist")
		return
	}
	ts := s.st.Transitions(key)
	out := make([]any, 0, len(ts))
	for _, t := range ts {
		st := s.st.Status(t.ToID)
		to := map[string]any{"id": t.ToID, "name": t.Name}
		if st != nil {
			to = s.statusJSON(*st)
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "to": to})
	}
	writeJSON(w, http.StatusOK, map[string]any{"transitions": out})
}

func (s *Server) postTransition(w http.ResponseWriter, r *http.Request, key string) {
	var body struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if err := s.st.Transition(key, body.Transition.ID); err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putAssignee(w http.ResponseWriter, r *http.Request, key string) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	id := ""
	if v, ok := body["accountId"]; ok && v != nil {
		id, _ = v.(string)
	}
	if v, ok := body["name"]; ok && v != nil && id == "" {
		id, _ = v.(string)
	}
	if err := s.st.SetAssignee(key, id); err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, "Issue does not exist")
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getEditMeta(w http.ResponseWriter, r *http.Request, key string) {
	fields, err := s.st.EditMeta(key)
	if err != nil {
		if store.IsNotFound(err) {
			writeJiraError(w, http.StatusNotFound, "Issue does not exist")
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

func (s *Server) postIssue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fields map[string]any `json:"fields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJiraError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	iss, err := s.st.CreateIssue(body.Fields)
	if err != nil {
		if fe, ok := store.AsFieldError(err); ok {
			writeJiraFieldErrors(w, map[string]string{fe.Field: fe.Msg})
			return
		}
		writeJiraError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": iss.ID, "key": iss.Key,
		"self": s.selfURL(r, s.cfg.Dialect.JiraPrefix()+"/issue/"+iss.Key),
	})
}

func (s *Server) postAttachment(w http.ResponseWriter, r *http.Request, key string) {
	if r.Header.Get("X-Atlassian-Token") == "" {
		writeJiraError(w, http.StatusForbidden, "XSRF check failed")
		return
	}
	ct := r.Header.Get("Content-Type")
	media, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(media, "multipart/") {
		writeJiraError(w, http.StatusBadRequest, "Expected multipart/form-data")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var created []any
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJiraError(w, http.StatusBadRequest, "Invalid multipart body")
			return
		}
		if part.FormName() != "file" {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(part, 8<<20))
		if err != nil {
			writeJiraError(w, http.StatusBadRequest, "Unable to read file")
			return
		}
		a, err := s.st.AddAttachment(key, part.FileName(), part.Header.Get("Content-Type"), s.identity(r).AccountID, b)
		if err != nil {
			if store.IsNotFound(err) {
				writeJiraError(w, http.StatusNotFound, "Issue does not exist")
				return
			}
			writeJiraError(w, http.StatusBadRequest, err.Error())
			return
		}
		created = append(created, s.attachJSON(r, a))
	}
	writeJSON(w, http.StatusOK, created)
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(id, "/")
	_, a := s.st.AttachmentBytes(id)
	if a == nil {
		writeJiraError(w, http.StatusNotFound, "Attachment not found")
		return
	}
	writeJSON(w, http.StatusOK, s.attachJSON(r, *a))
}

func (s *Server) getAttachmentContent(w http.ResponseWriter, r *http.Request, id string) {
	id = strings.Trim(id, "/")
	_, a := s.st.AttachmentBytes(id)
	if a == nil {
		writeJiraError(w, http.StatusNotFound, "Attachment not found")
		return
	}
	loc := s.selfURL(r, "/file/"+a.MediaID+"/binary?name="+a.Filename)
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}
