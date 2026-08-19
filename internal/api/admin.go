package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/midagedev/issuetap/internal/diagnostics"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/scenarios"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	// Lab surface: no Atlassian credential required. Bound to loopback by default.
	path := strings.TrimPrefix(r.URL.Path, "/api")
	switch {
	case r.Method == http.MethodGet && path == "/overview":
		s.apiOverview(w, r)
	case r.Method == http.MethodGet && path == "/requests":
		s.apiRequests(w, r)
	case r.Method == http.MethodGet && path == "/data":
		s.apiData(w, r)
	case r.Method == http.MethodGet && path == "/diff":
		s.apiDiff(w, r)
	case r.Method == http.MethodGet && path == "/compatibility":
		s.apiCompatibility(w, r)
	case r.Method == http.MethodGet && path == "/diagnostics":
		s.apiDiagnostics(w, r)
	case r.Method == http.MethodPost && path == "/fixtures/apply":
		s.apiApply(w, r)
	case r.Method == http.MethodGet && path == "/fixtures/snapshot":
		s.apiSnapshot(w, r)
	case r.Method == http.MethodPost && path == "/scenarios/run":
		s.apiScenario(w, r)
	case r.Method == http.MethodPost && path == "/locale":
		s.apiSetLocale(w, r)
	case r.Method == http.MethodPost && path == "/faults":
		s.apiSetFaults(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown admin path", "path": path})
	}
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"product": "issuetap",
		"dialect": s.cfg.Dialect.Kind,
		"context": s.cfg.Dialect.ContextPath,
		"locale":  s.st.Locale(),
		"seed":    s.st.Seed(),
		"counts":  s.st.Counts(),
		"faults":  s.eng.Stats(),
		"ui":      s.uiOK,
	})
}

func (s *Server) apiRequests(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	writeJSON(w, http.StatusOK, map[string]any{"requests": s.log.list(limit)})
}

func (s *Server) apiData(w http.ResponseWriter, r *http.Request) {
	type issueRow struct {
		Key, Summary, Status, StatusID, Type, TypeID, Project string
		Comments, Histories                                   int
	}
	var issues []issueRow
	for _, p := range s.st.Projects() {
		_ = p
	}
	// Pull via search-all.
	all, _, err := s.st.Search("", 0, -1)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	for _, iss := range all {
		stName, typName := "", ""
		if st := s.st.Status(iss.StatusID); st != nil {
			stName = st.Name
		}
		if t := s.st.IssueType(iss.IssueTypeID); t != nil {
			typName = t.Name
		}
		issues = append(issues, issueRow{
			Key: iss.Key, Summary: iss.Summary, Status: stName, StatusID: iss.StatusID,
			Type: typName, TypeID: iss.IssueTypeID, Project: iss.ProjectKey,
			Comments: len(iss.Comments), Histories: len(iss.Histories),
		})
	}
	type pageRow struct {
		ID, Title, Space, Type string
		Comments               int
	}
	var pages []pageRow
	for _, p := range s.st.Pages() {
		pages = append(pages, pageRow{
			ID: p.ID, Title: p.Title, Space: p.SpaceKey, Type: p.Type,
			Comments: len(s.st.ChildComments(p.ID)),
		})
	}
	type userRow struct {
		AccountID, DisplayName, Email string
	}
	var users []userRow
	for _, u := range s.st.Users() {
		users = append(users, userRow{u.AccountID, u.DisplayName, u.Email})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"projects":      s.st.Projects(),
		"issues":        issues,
		"pages":         pages,
		"users":         users,
		"statuses":      s.st.Statuses(),
		"fieldRegistry": s.st.FieldRegistry(),
	})
}

func (s *Server) apiDiff(w http.ResponseWriter, r *http.Request) {
	last := s.log.last()
	if last == nil {
		writeJSON(w, http.StatusOK, map[string]any{"message": "no requests yet"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request": last,
		"note":    "Compare request body to the documented shape in docs/COMPATIBILITY.md. issuetap does not invent provider fields the fixture did not supply.",
	})
}

func (s *Server) apiCompatibility(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"routes": Inventory()})
}

func (s *Server) apiDiagnostics(w http.ResponseWriter, r *http.Request) {
	b, err := diagnostics.Build(diagnostics.Input{
		Dialect:  string(s.cfg.Dialect.Kind),
		Locale:   string(s.st.Locale()),
		Seed:     s.st.Seed(),
		Counts:   s.st.Counts(),
		Traces:   toAnyTraces(s.log.list(200)),
		Snapshot: s.st.Snapshot(),
		Faults:   s.eng.Stats(),
		Routes:   Inventory(),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="issuetap-diagnose.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func toAnyTraces(in []Trace) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, t := range in {
		out = append(out, map[string]any{
			"id": t.ID, "at": t.At, "method": t.Method, "path": t.Path,
			"status": t.Status, "latencyMs": t.LatencyMS, "fault": t.Fault,
		})
	}
	return out
}

func (s *Server) apiApply(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	doc, err := fixtures.Parse(b, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.st.Apply(doc); err != nil {
		writeJSONWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "counts": s.st.Counts()})
}

func (s *Server) apiSnapshot(w http.ResponseWriter, r *http.Request) {
	doc := s.st.Snapshot()
	if r.URL.Query().Get("format") == "json" {
		b, err := fixtures.MarshalJSON(doc)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
		return
	}
	b, err := fixtures.MarshalYAML(doc)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (s *Server) apiScenario(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	sc, err := scenarios.Parse(b)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(sc.Faults) > 0 {
		s.eng.Replace(sc.Faults)
	}
	if sc.Locale != "" {
		if err := s.st.SetLocale(locale.Parse(sc.Locale)); err != nil {
			writeJSONWriteError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"name":   sc.Name,
		"faults": len(sc.Faults),
		"locale": s.st.Locale(),
	})
}

func (s *Server) apiSetLocale(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := s.st.SetLocale(locale.Parse(body.Locale)); err != nil {
		writeJSONWriteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locale": s.st.Locale()})
}

func (s *Server) apiSetFaults(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Faults []faults.Fault `json:"faults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.eng.Replace(body.Faults)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "faults": s.eng.Stats()})
}
