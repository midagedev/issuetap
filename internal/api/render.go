package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/adf"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
	"github.com/midagedev/issuetap/internal/store"
)

func (s *Server) selfURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + host + path
}

func (s *Server) userJSON(u model.User) map[string]any {
	if s.cfg.Dialect.Kind == dialect.DC {
		name := u.Name
		if name == "" {
			name = u.AccountID
		}
		key := u.Key
		if key == "" {
			key = name
		}
		return map[string]any{
			"name":         name,
			"key":          key,
			"displayName":  u.DisplayName,
			"emailAddress": u.Email,
			"active":       u.Active,
			"avatarUrls":   u.AvatarURLs,
		}
	}
	return map[string]any{
		"accountId":    u.AccountID,
		"accountType":  firstNonEmpty(u.AccountType, "atlassian"),
		"displayName":  u.DisplayName,
		"emailAddress": u.Email,
		"active":       u.Active,
		"timeZone":     firstNonEmpty(u.TimeZone, "Asia/Seoul"),
		"locale":       locale.BCP47(s.st.Locale()),
		"avatarUrls":   u.AvatarURLs,
	}
}

func (s *Server) statusJSON(st model.Status) map[string]any {
	st = locale.OverlayStatus(s.st.Locale(), st)
	return map[string]any{
		"id":               st.ID,
		"name":             st.Name,
		"untranslatedName": firstNonEmpty(st.Untranslated, st.Name),
		"statusCategory": map[string]any{
			"id":        st.StatusCategory.ID,
			"key":       st.StatusCategory.Key,
			"name":      st.StatusCategory.Name,
			"colorName": st.StatusCategory.ColorName,
		},
	}
}

func (s *Server) typeJSON(t model.IssueType) map[string]any {
	t = locale.OverlayIssueType(s.st.Locale(), t)
	return map[string]any{
		"id":               t.ID,
		"name":             t.Name,
		"untranslatedName": firstNonEmpty(t.Untranslated, t.Name),
		"hierarchyLevel":   t.HierarchyLevel,
		"subtask":          t.Subtask,
	}
}

func (s *Server) priorityJSON(p model.Priority) map[string]any {
	// No overlay here: every priority this renders already came from a
	// store accessor, and the store is the single owner of the priority
	// locale (Store.prioLoc — serve trap vs embedded Cloud fidelity,
	// gadak GDK-597). Re-overlaying here would translate an
	// English-pinned name back.
	out := map[string]any{"id": p.ID, "name": p.Name}
	if p.StatusColor != "" {
		out["statusColor"] = p.StatusColor
	}
	return out
}

func (s *Server) issueJSON(r *http.Request, iss *model.Issue, fields []string, expand string) map[string]any {
	want := fieldSet(fields)
	all := want["*all"] || len(want) == 0
	loc := s.st.Locale()

	proj := s.st.Project(iss.ProjectKey)
	projObj := map[string]any{"key": iss.ProjectKey}
	if proj != nil {
		projObj["id"] = proj.ID
		projObj["name"] = proj.Name
		projObj["projectTypeKey"] = proj.TypeKey
	}

	st := s.st.Status(iss.StatusID)
	var statusObj map[string]any
	if st != nil {
		statusObj = s.statusJSON(*st)
	} else {
		statusObj = map[string]any{"id": iss.StatusID, "name": iss.StatusID}
	}

	it := s.st.IssueType(iss.IssueTypeID)
	var typeObj map[string]any
	if it != nil {
		typeObj = s.typeJSON(*it)
	} else {
		typeObj = map[string]any{"id": iss.IssueTypeID, "name": iss.IssueTypeID}
	}

	var prioObj map[string]any
	if iss.PriorityID != "" {
		if p := s.st.Priority(iss.PriorityID); p != nil {
			prioObj = s.priorityJSON(*p)
		} else {
			prioObj = map[string]any{"id": iss.PriorityID, "name": iss.PriorityID}
		}
	}

	var assignee any
	if iss.AssigneeID != "" {
		if u := s.st.User(iss.AssigneeID); u != nil {
			assignee = s.userJSON(*u)
		}
	}
	var reporter any
	if iss.ReporterID != "" {
		if u := s.st.User(iss.ReporterID); u != nil {
			reporter = s.userJSON(*u)
		}
	}
	var creator any
	if iss.CreatorID != "" {
		if u := s.st.User(iss.CreatorID); u != nil {
			creator = s.userJSON(*u)
		}
	}

	desc := s.bodyForDialect(iss.DescriptionADF, iss.DescriptionText)
	env := s.bodyForDialect(iss.EnvironmentADF, iss.EnvironmentText)

	comments := make([]any, 0, len(iss.Comments))
	for _, c := range iss.Comments {
		comments = append(comments, s.commentJSON(c))
	}
	atts := make([]any, 0, len(iss.Attachments))
	for _, a := range iss.Attachments {
		atts = append(atts, s.attachJSON(r, a))
	}
	links := make([]any, 0, len(iss.Links))
	for _, l := range iss.Links {
		obj := map[string]any{"type": issueLinkTypeFields(l.TypeName)}
		// The id is synthetic but stable: typeID:outwardEnd:inwardEnd, the
		// same string from either projection, so DELETE /issueLink/{id} can
		// name the link with what any one GET handed out. An element labels
		// the OTHER end by that end's role, so {outwardIssue: B} on X means
		// B is the outward end — and an old-convention persist row pairs up
		// to the identical id, which is why deletion needs no migration.
		if tid, _ := issueLinkTypeFields(l.TypeName)["id"].(string); tid != "" {
			if l.OutwardKey != "" {
				obj["id"] = issueLinkID(tid, l.OutwardKey, iss.Key)
			} else if l.InwardKey != "" {
				obj["id"] = issueLinkID(tid, iss.Key, l.InwardKey)
			}
		}
		if l.OutwardKey != "" {
			obj["outwardIssue"] = map[string]any{"key": l.OutwardKey}
		}
		if l.InwardKey != "" {
			obj["inwardIssue"] = map[string]any{"key": l.InwardKey}
		}
		links = append(links, obj)
	}

	full := map[string]any{
		"summary":     iss.Summary,
		"description": desc,
		"environment": env,
		"issuetype":   typeObj,
		"status":      statusObj,
		"priority":    prioObj,
		"assignee":    assignee,
		"reporter":    reporter,
		"creator":     creator,
		"project":     projObj,
		"labels":      iss.Labels,
		"components":  namedArr(iss.Components),
		"fixVersions": namedArr(iss.FixVersions),
		"versions":    namedArr(iss.Versions),
		"duedate":     emptyNil(iss.Duedate),
		"created":     iss.Created,
		"updated":     iss.Updated,
		"comment": map[string]any{
			"comments":   comments,
			"total":      len(iss.Comments),
			"maxResults": len(iss.Comments),
			"startAt":    0,
		},
		"attachment": atts,
		"issuelinks": links,
	}
	if iss.ParentKey != "" {
		full["parent"] = map[string]any{"key": iss.ParentKey}
	}
	if iss.ResolutionID != "" {
		if res := resolutionByID(s.st, iss.ResolutionID); res != nil {
			cp := locale.OverlayResolution(loc, *res)
			full["resolution"] = map[string]any{"id": cp.ID, "name": cp.Name}
		} else {
			full["resolution"] = map[string]any{"id": iss.ResolutionID, "name": iss.ResolutionID}
		}
	} else {
		full["resolution"] = nil
	}
	for k, v := range iss.Custom {
		full[k] = v
	}

	fieldsOut := map[string]any{}
	if all {
		fieldsOut = full
	} else {
		for k, v := range full {
			if want[k] {
				fieldsOut[k] = v
			}
		}
		// Always include summary if they asked for anything — gadak reconcile
		// asks for summary only.
	}

	out := map[string]any{
		"id":     iss.ID,
		"key":    iss.Key,
		"self":   s.selfURL(r, s.cfg.Dialect.JiraPrefix()+"/issue/"+iss.Key),
		"fields": fieldsOut,
	}
	if strings.Contains(expand, "changelog") {
		out["changelog"] = s.changelogObj(iss, 0, len(iss.Histories))
	}
	return out
}

func resolutionByID(st *store.Store, id string) *model.Resolution {
	for _, r := range st.Resolutions() {
		if r.ID == id {
			cp := r
			return &cp
		}
	}
	return nil
}

func (s *Server) bodyForDialect(raw json.RawMessage, text string) any {
	if s.cfg.Dialect.UsesADF() {
		if len(raw) == 0 {
			if text == "" {
				return nil
			}
			return json.RawMessage(adf.Doc(text))
		}
		return json.RawMessage(raw)
	}
	if text != "" {
		return text
	}
	return adf.Plain(raw)
}

func (s *Server) commentJSON(c model.Comment) map[string]any {
	out := map[string]any{
		"id":      c.ID,
		"author":  s.userJSON(c.Author),
		"body":    s.bodyForDialect(c.Body, c.BodyText),
		"created": c.Created,
		"updated": c.Updated,
	}
	if c.Visibility != nil {
		out["visibility"] = map[string]any{
			"type":  c.Visibility.Type,
			"value": c.Visibility.Value,
		}
	}
	if c.JsdPublic != nil {
		out["jsdPublic"] = *c.JsdPublic
	}
	return out
}

func (s *Server) attachJSON(r *http.Request, a model.Attachment) map[string]any {
	return map[string]any{
		"id":       a.ID,
		"filename": a.Filename,
		"mimeType": a.MimeType,
		"size":     a.Size,
		"author":   s.userJSON(a.Author),
		"created":  a.Created,
		"content":  s.selfURL(r, s.cfg.Dialect.JiraPrefix()+"/attachment/content/"+a.ID),
	}
}

func (s *Server) changelogObj(iss *model.Issue, start, max int) map[string]any {
	total := len(iss.Histories)
	if start < 0 {
		start = 0
	}
	if max <= 0 {
		max = 100
	}
	end := start + max
	if end > total {
		end = total
	}
	var values []any
	if start < end {
		for _, h := range iss.Histories[start:end] {
			values = append(values, s.historyJSON(h))
		}
	} else {
		values = []any{}
	}
	return map[string]any{
		"startAt":    start,
		"maxResults": max,
		"total":      total,
		"isLast":     end >= total,
		"histories":  values, // inline expand=changelog
		"values":     values, // dedicated /changelog endpoint
	}
}

func (s *Server) historyJSON(h model.History) map[string]any {
	loc := s.st.Locale()
	items := make([]any, 0, len(h.Items))
	for _, it := range h.Items {
		field := locale.ChangelogField(loc, it.FieldID, it.Field)
		fromS, toS := it.FromString, it.ToString
		// Re-localize from/to strings for status/priority/type ids.
		if it.FieldID == "status" {
			if st := s.st.Status(it.From); st != nil {
				fromS = st.Name
			}
			if st := s.st.Status(it.To); st != nil {
				toS = st.Name
			}
		}
		if it.FieldID == "priority" {
			if p := s.st.Priority(it.From); p != nil {
				fromS = p.Name
			}
			if p := s.st.Priority(it.To); p != nil {
				toS = p.Name
			}
		}
		items = append(items, map[string]any{
			"field": field, "fieldId": it.FieldID,
			"from": it.From, "fromString": fromS,
			"to": it.To, "toString": toS,
		})
	}
	return map[string]any{
		"id": h.ID, "created": h.Created, "author": s.userJSON(h.Author), "items": items,
	}
}

// issueLinkID is the synthetic wire id for one link: typeID:outwardEnd:inwardEnd.
func issueLinkID(typeID, outwardKey, inwardKey string) string {
	return typeID + ":" + outwardKey + ":" + inwardKey
}

func issueLinkTypeFields(name string) map[string]any {
	obj := map[string]any{"name": name}
	for _, lt := range model.DefaultIssueLinkTypes() {
		if lt.Name == name {
			obj["id"] = lt.ID
			obj["inward"] = lt.Inward
			obj["outward"] = lt.Outward
			return obj
		}
	}
	return obj
}

func namedArr(in []model.Named) []any {
	out := make([]any, 0, len(in))
	for _, n := range in {
		out = append(out, map[string]any{"id": n.ID, "name": n.Name})
	}
	return out
}

func fieldSet(fields []string) map[string]bool {
	out := map[string]bool{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		out[f] = true
	}
	return out
}

func emptyNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
