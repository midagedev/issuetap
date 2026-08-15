package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/model"
)

func (s *Server) handleConfluence(w http.ResponseWriter, r *http.Request, path string) {
	// path is /rest/api/...
	suffix := strings.TrimPrefix(path, "/rest/api")
	if suffix == path {
		// maybe /wiki already stripped, leftover /rest/api
		writeUnsupported(w, r.Method, r.URL.Path)
		return
	}
	switch {
	case r.Method == http.MethodGet && (suffix == "/space" || suffix == "/space/"):
		s.getSpaces(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/space/"):
		s.getSpace(w, r, strings.TrimPrefix(suffix, "/space/"))
	case r.Method == http.MethodGet && suffix == "/content/search":
		s.getContentSearch(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(suffix, "/content/"):
		s.handleContent(w, r, strings.TrimPrefix(suffix, "/content/"))
	default:
		writeUnsupported(w, r.Method, r.URL.Path)
	}
}

func (s *Server) getSpaces(w http.ResponseWriter, r *http.Request) {
	start := atoiDefault(r.URL.Query().Get("start"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 25)
	if limit <= 0 {
		limit = 25
	}
	all := s.st.Spaces(false)
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	results := make([]any, 0, end-start)
	for _, sp := range all[start:end] {
		results = append(results, s.spaceJSON(r, sp))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"start":   start,
		"limit":   limit,
		"size":    len(results),
		"_links":  map[string]any{"base": s.wikiBase(r)},
	})
}

func (s *Server) getSpace(w http.ResponseWriter, r *http.Request, key string) {
	key = strings.Split(key, "?")[0]
	key = strings.Trim(key, "/")
	sp := s.st.Space(key)
	if sp == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"statusCode": 404, "data": map[string]any{"authorized": false},
			"message": "No space found with key: " + key,
		})
		return
	}
	writeJSON(w, http.StatusOK, s.spaceJSON(r, *sp))
}

func (s *Server) spaceJSON(r *http.Request, sp model.Space) map[string]any {
	out := map[string]any{
		"id":     sp.ID,
		"key":    sp.Key,
		"name":   sp.Name,
		"type":   sp.Type,
		"status": firstNonEmpty(sp.Status, "current"),
	}
	if sp.HomepageID != "" {
		out["homepage"] = map[string]any{"id": sp.HomepageID}
	}
	return out
}

func (s *Server) getContentSearch(w http.ResponseWriter, r *http.Request) {
	cql := r.URL.Query().Get("cql")
	limit := atoiDefault(r.URL.Query().Get("limit"), 25)
	if limit <= 0 {
		limit = 25
	}
	start := atoiDefault(r.URL.Query().Get("start"), 0)
	// Cursor-style next=true&cursor=N (we use the offset as cursor).
	if r.URL.Query().Get("next") == "true" {
		if c := r.URL.Query().Get("cursor"); c != "" {
			if n, err := strconv.Atoi(c); err == nil {
				start = n
			}
		}
	}
	all, err := s.st.SearchPages(cql)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"statusCode": 400, "message": err.Error(),
		})
		return
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	results := make([]any, 0, end-start)
	for _, p := range all[start:end] {
		results = append(results, s.pageHitJSON(r, p, r.URL.Query().Get("expand")))
	}
	links := map[string]any{"base": s.wikiBase(r)}
	if end < len(all) {
		// gadak's nextPath accepts /wiki/rest/... or /rest/... relative to wiki base.
		links["next"] = "/rest/api/content/search?next=true&cursor=" + strconv.Itoa(end) + "&cql=" + cql + "&limit=" + strconv.Itoa(limit)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"start":   start,
		"limit":   limit,
		"size":    len(results),
		"_links":  links,
	})
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request, rest string) {
	id, extra, _ := strings.Cut(rest, "/")
	id = strings.TrimSpace(id)
	if extra == "child/comment" || strings.HasPrefix(extra, "child/comment") {
		s.getChildComments(w, r, id)
		return
	}
	if extra != "" {
		writeUnsupported(w, r.Method, r.URL.Path)
		return
	}
	s.getPage(w, r, id)
}

func (s *Server) getPage(w http.ResponseWriter, r *http.Request, id string) {
	p := s.st.Page(id)
	if p == nil {
		// Comments are also content ids (gadak fallback GET).
		// Search all comments.
		for _, pg := range s.st.Pages() {
			for _, cm := range s.st.ChildComments(pg.ID) {
				if cm.ID == id {
					writeJSON(w, http.StatusOK, s.commentContentJSON(r, pg, cm))
					return
				}
				for _, reply := range s.st.ChildComments(cm.ID) {
					if reply.ID == id {
						writeJSON(w, http.StatusOK, s.commentContentJSON(r, pg, reply))
						return
					}
				}
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{
			"statusCode": 404, "message": "No content found with id: " + id,
		})
		return
	}
	writeJSON(w, http.StatusOK, s.pageFullJSON(r, *p, r.URL.Query().Get("expand")))
}

func (s *Server) getChildComments(w http.ResponseWriter, r *http.Request, id string) {
	start := atoiDefault(r.URL.Query().Get("start"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 25)
	if limit <= 0 {
		limit = 25
	}
	all := s.st.ChildComments(id)
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	results := make([]any, 0, end-start)
	for _, c := range all[start:end] {
		results = append(results, s.pageCommentJSON(r, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results, "start": start, "limit": limit, "size": len(results),
	})
}

func (s *Server) wikiBase(r *http.Request) string {
	if s.cfg.Dialect.Kind == "dc" {
		return s.selfURL(r, s.cfg.Dialect.WikiPrefix())
	}
	return s.selfURL(r, "/wiki")
}

func (s *Server) pageHitJSON(r *http.Request, p model.Page, expand string) map[string]any {
	out := map[string]any{
		"id":     p.ID,
		"type":   firstNonEmpty(p.Type, "page"),
		"status": firstNonEmpty(p.Status, "current"),
		"title":  p.Title,
		"space":  s.spaceRef(p.SpaceKey),
		"version": s.versionJSON(p.Version, p.When, p.AuthorID),
		"_links": map[string]any{
			"webui": firstNonEmpty(p.WebUI, "/spaces/"+p.SpaceKey+"/pages/"+p.ID),
		},
	}
	return out
}

func (s *Server) pageFullJSON(r *http.Request, p model.Page, expand string) map[string]any {
	out := s.pageHitJSON(r, p, expand)
	exp := strings.ToLower(expand)
	if exp == "" || strings.Contains(exp, "body.atlas_doc_format") || strings.Contains(exp, "body") {
		if s.cfg.Dialect.UsesADF() {
			val := ""
			if len(p.BodyADF) > 0 {
				val = string(p.BodyADF)
			}
			out["body"] = map[string]any{
				"atlas_doc_format": map[string]any{
					"value":          val,
					"representation": "atlas_doc_format",
				},
			}
		} else {
			out["body"] = map[string]any{
				"storage": map[string]any{
					"value":          firstNonEmpty(p.BodyStorage, p.BodyText),
					"representation": "storage",
				},
			}
		}
	}
	if exp == "" || strings.Contains(exp, "ancestors") {
		anc := make([]any, 0, len(p.Ancestors))
		for _, id := range p.Ancestors {
			anc = append(anc, map[string]any{"id": id})
		}
		out["ancestors"] = anc
	}
	if strings.Contains(exp, "metadata.labels") || strings.Contains(exp, "metadata") {
		labs := make([]any, 0, len(p.Labels))
		for i, n := range p.Labels {
			labs = append(labs, map[string]any{"name": n, "prefix": "global", "id": strconv.Itoa(i + 1)})
		}
		out["metadata"] = map[string]any{
			"labels": map[string]any{"results": labs, "size": len(labs), "limit": 25, "start": 0},
		}
	}
	return out
}

func (s *Server) pageCommentJSON(r *http.Request, c model.PageComment) map[string]any {
	val := ""
	if len(c.BodyADF) > 0 {
		val = string(c.BodyADF)
	}
	body := map[string]any{}
	if s.cfg.Dialect.UsesADF() {
		body["atlas_doc_format"] = map[string]any{"value": val, "representation": "atlas_doc_format"}
	} else {
		body["storage"] = map[string]any{"value": "<p>" + c.BodyText + "</p>", "representation": "storage"}
	}
	return map[string]any{
		"id":      c.ID,
		"title":   c.Title,
		"type":    "comment",
		"status":  "current",
		"body":    body,
		"version": s.versionJSON(c.Version, c.When, c.AuthorID),
	}
}

func (s *Server) commentContentJSON(r *http.Request, pg model.Page, c model.PageComment) map[string]any {
	out := s.pageCommentJSON(r, c)
	out["ancestors"] = []any{map[string]any{"id": pg.ID}}
	out["space"] = s.spaceRef(pg.SpaceKey)
	out["_links"] = map[string]any{
		"webui": "/spaces/" + pg.SpaceKey + "/pages/" + pg.ID + "?pageId=" + pg.ID,
	}
	return out
}

func (s *Server) spaceRef(key string) map[string]any {
	if sp := s.st.Space(key); sp != nil {
		return map[string]any{"key": sp.Key, "name": sp.Name, "type": sp.Type, "id": sp.ID}
	}
	return map[string]any{"key": key}
}

func (s *Server) versionJSON(n int, when, authorID string) map[string]any {
	if n <= 0 {
		n = 1
	}
	by := map[string]any{}
	if u := s.st.User(authorID); u != nil {
		by = s.userJSON(*u)
	} else if authorID != "" {
		by = map[string]any{"accountId": authorID, "displayName": authorID}
	}
	return map[string]any{"number": n, "when": when, "by": by}
}
