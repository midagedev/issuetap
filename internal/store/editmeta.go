package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

// Registry kinds. option / option-array / text / number / date / user
// match the spec; schema.type/items is the Jira Cloud shape gadak reads.
const (
	KindOption      = "option"
	KindOptionArray = "option-array"
	KindText        = "text"
	KindNumber      = "number"
	KindDate        = "date"
	KindUser        = "user"
)

type editFieldSpec struct {
	id         string
	required   bool
	operations []string
	typ        string
	items      string
	allowed    string // priority | issuetype | fixVersions | components | ""
}

func systemEditSpecs() []editFieldSpec {
	return []editFieldSpec{
		{id: "summary", required: true, operations: []string{"set"}, typ: "string"},
		{id: "description", operations: []string{"set"}, typ: "string"},
		{id: "labels", operations: []string{"add", "set", "remove"}, typ: "array", items: "string"},
		{id: "priority", operations: []string{"set"}, typ: "priority", allowed: "priority"},
		{id: "assignee", operations: []string{"set"}, typ: "user"},
		{id: "duedate", operations: []string{"set"}, typ: "date"},
		{id: "parent", operations: []string{"set"}, typ: "issuelink"},
		{id: "issuetype", operations: []string{"set"}, typ: "issuetype", allowed: "issuetype"},
		{id: "fixVersions", operations: []string{"add", "set", "remove"}, typ: "array", items: "version", allowed: "fixVersions"},
		{id: "components", operations: []string{"add", "set", "remove"}, typ: "array", items: "component", allowed: "components"},
	}
}

type createFieldSpec struct {
	id         string
	required   bool
	hasDefault bool
	operations []string
	typ        string
	items      string
	allowed    string // priority | issuetype | fixVersions | components | ""
}

// systemCreateSpecs is what CreateIssue actually requires and fills.
// required/hasDefaultValue must stay in lockstep with CreateIssue.
func systemCreateSpecs() []createFieldSpec {
	return []createFieldSpec{
		{id: "project", required: true, operations: []string{"set"}, typ: "project"},
		{id: "summary", required: true, operations: []string{"set"}, typ: "string"},
		{id: "issuetype", required: true, hasDefault: true, operations: []string{"set"}, typ: "issuetype", allowed: "issuetype"},
		{id: "reporter", required: true, hasDefault: true, operations: []string{"set"}, typ: "user"},
		{id: "priority", hasDefault: true, operations: []string{"set"}, typ: "priority", allowed: "priority"},
		{id: "description", operations: []string{"set"}, typ: "string"},
		{id: "labels", operations: []string{"add", "set", "remove"}, typ: "array", items: "string"},
		{id: "assignee", operations: []string{"set"}, typ: "user"},
		{id: "duedate", operations: []string{"set"}, typ: "date"},
		{id: "parent", operations: []string{"set"}, typ: "issuelink"},
		{id: "fixVersions", operations: []string{"add", "set", "remove"}, typ: "array", items: "version", allowed: "fixVersions"},
		{id: "components", operations: []string{"add", "set", "remove"}, typ: "array", items: "component", allowed: "components"},
	}
}

func (s *Store) priorityAllowedValues() []any {
	av := make([]any, 0)
	for _, p := range s.Priorities() {
		row := map[string]any{"id": p.ID, "name": p.Name}
		if p.StatusColor != "" {
			row["statusColor"] = p.StatusColor
		}
		av = append(av, row)
	}
	return av
}

func (s *Store) issueTypeAllowedValues() []any {
	av := make([]any, 0)
	for _, t := range s.IssueTypes() {
		av = append(av, map[string]any{
			"id": t.ID, "name": t.Name,
			"untranslatedName": first(t.Untranslated, t.Name),
			"hierarchyLevel":   t.HierarchyLevel,
			"subtask":          t.Subtask,
		})
	}
	return av
}

func (s *Store) attachAllowed(meta map[string]any, allowed, project string) {
	switch allowed {
	case "priority":
		meta["allowedValues"] = s.priorityAllowedValues()
	case "issuetype":
		meta["allowedValues"] = s.issueTypeAllowedValues()
	case "fixVersions", "components":
		meta["allowedValues"] = s.namedAllowedValues(project, allowed)
	}
}

func (s *Store) namedAllowedValues(project, field string) []any {
	s.mu.RLock()
	cat := s.projectNamedCatalogLocked(project, field)
	s.mu.RUnlock()
	sort.Slice(cat, func(i, j int) bool { return cat[i].ID < cat[j].ID })
	av := make([]any, 0, len(cat))
	for _, n := range cat {
		av = append(av, map[string]any{"id": n.ID, "name": n.Name})
	}
	return av
}

// EditMeta is the fields object of GET /issue/{key}/editmeta.
// System fields and registered custom fields share this table; UpdateIssue
// accepts every id this function advertises.
func (s *Store) EditMeta(key string) (map[string]any, error) {
	iss := s.Issue(key)
	if iss == nil {
		return nil, errNotFound("issue", key)
	}
	project := iss.ProjectKey
	out := map[string]any{}
	for _, spec := range systemEditSpecs() {
		schema := map[string]any{"type": spec.typ, "system": spec.id}
		if spec.items != "" {
			schema["items"] = spec.items
		}
		meta := map[string]any{
			"required":   spec.required,
			"operations": append([]string{}, spec.operations...),
			"schema":     schema,
		}
		s.attachAllowed(meta, spec.allowed, project)
		out[spec.id] = meta
	}
	for _, f := range s.Fields() {
		if !f.Custom {
			continue
		}
		out[f.ID] = customEditMeta(f)
	}
	return out, nil
}

// CreateFields is the fields list of
// GET /issue/createmeta/{projectIdOrKey}/issuetypes/{issueTypeId}.
// Flags are derived from CreateIssue so advertisement cannot drift from
// acceptance. Pagination is applied by the HTTP handler.
func (s *Store) CreateFields(projectIDOrKey, issueTypeID string) ([]map[string]any, error) {
	proj := s.projectByIDOrKey(projectIDOrKey)
	if proj == nil {
		return nil, errNotFound("project", projectIDOrKey)
	}
	if s.IssueType(issueTypeID) == nil {
		return nil, errNotFound("issuetype", issueTypeID)
	}
	loc := s.Locale()
	out := make([]map[string]any, 0, 16)
	for _, spec := range systemCreateSpecs() {
		schema := map[string]any{"type": spec.typ, "system": spec.id}
		if spec.items != "" {
			schema["items"] = spec.items
		}
		row := map[string]any{
			"fieldId":         spec.id,
			"key":             spec.id,
			"name":            locale.FieldName(loc, spec.id, spec.id),
			"required":        spec.required,
			"hasDefaultValue": spec.hasDefault,
			"operations":      append([]string{}, spec.operations...),
			"schema":          schema,
		}
		s.attachAllowed(row, spec.allowed, proj.Key)
		out = append(out, row)
	}
	for _, f := range s.Fields() {
		if !f.Custom {
			continue
		}
		meta := customEditMeta(f)
		row := map[string]any{
			"fieldId":         f.ID,
			"key":             f.ID,
			"name":            f.Name,
			"required":        false,
			"hasDefaultValue": false,
			"operations":      meta["operations"],
			"schema":          meta["schema"],
		}
		if av, ok := meta["allowedValues"]; ok {
			row["allowedValues"] = av
		}
		out = append(out, row)
	}
	return out, nil
}

func customEditMeta(f model.FieldInfo) map[string]any {
	kind := fieldKind(f)
	typ, items := schemaTypeItems(kind, f.Schema)
	ops := []string{"set"}
	if kind == KindOptionArray {
		ops = []string{"add", "set", "remove"}
	}
	schema := map[string]any{"type": typ}
	if items != "" {
		schema["items"] = items
	}
	if f.Schema.Custom != "" {
		schema["custom"] = f.Schema.Custom
	}
	if n := parseCustomID(f.ID); n > 0 {
		schema["customId"] = n
	}
	meta := map[string]any{
		"required":   false,
		"operations": ops,
		"schema":     schema,
	}
	if kind == KindOption || kind == KindOptionArray {
		av := make([]any, 0, len(f.Options))
		for _, o := range f.Options {
			av = append(av, map[string]any{"id": o.ID, "value": o.Value})
		}
		meta["allowedValues"] = av
	}
	return meta
}

func parseCustomID(id string) int {
	const p = "customfield_"
	if !strings.HasPrefix(id, p) {
		return 0
	}
	n, err := strconv.Atoi(id[len(p):])
	if err != nil {
		return 0
	}
	return n
}

func fieldKind(f model.FieldInfo) string {
	switch f.Schema.Type {
	case KindOption, "select":
		return KindOption
	case KindOptionArray, "multiselect":
		return KindOptionArray
	case "array":
		if f.Schema.Items == "option" {
			return KindOptionArray
		}
	case KindText, "string", "textfield":
		return KindText
	case KindNumber, "float":
		return KindNumber
	case KindDate:
		return KindDate
	case KindUser:
		return KindUser
	}
	return f.Schema.Type
}

func schemaTypeItems(kind string, schema model.FieldSchema) (typ, items string) {
	switch kind {
	case KindOption:
		return "option", ""
	case KindOptionArray:
		return "array", "option"
	case KindText:
		return "string", ""
	case KindNumber:
		return "number", ""
	case KindDate:
		return "date", ""
	case KindUser:
		return "user", ""
	}
	return schema.Type, schema.Items
}

var customTypeURI = map[string]string{
	KindOption:      "com.atlassian.jira.plugin.system.customfieldtypes:select",
	KindOptionArray: "com.atlassian.jira.plugin.system.customfieldtypes:multiselect",
	KindText:        "com.atlassian.jira.plugin.system.customfieldtypes:textfield",
	KindNumber:      "com.atlassian.jira.plugin.system.customfieldtypes:float",
	KindDate:        "com.atlassian.jira.plugin.system.customfieldtypes:datepicker",
	KindUser:        "com.atlassian.jira.plugin.system.customfieldtypes:userpicker",
}

func kindFromFixture(f fixtures.Field) string {
	switch f.Type {
	case KindOptionArray, "multiselect":
		return KindOptionArray
	case KindOption, "select":
		return KindOption
	case KindText, "textfield":
		return KindText
	case "array":
		if f.Items == "option" {
			return KindOptionArray
		}
	case "string":
		return KindText
	case KindNumber, "float":
		return KindNumber
	case KindDate:
		return KindDate
	case KindUser:
		return KindUser
	}
	if f.Type != "" {
		return f.Type
	}
	return KindText
}

func (s *Store) upsertField(f fixtures.Field) {
	kind := kindFromFixture(f)
	typ, items := schemaTypeItems(kind, model.FieldSchema{Type: f.Type, Items: f.Items})
	if f.Type == "array" && f.Items != "" && kind != KindOptionArray {
		typ, items = "array", f.Items
	}
	info := model.FieldInfo{
		ID: f.ID, Key: f.ID, Name: f.Name, Custom: f.Custom,
		Schema:    model.FieldSchema{Type: typ, Items: items},
		Orderable: true, Navigable: true, Searchable: true,
		Clause: []string{f.ID},
	}
	if f.Custom {
		if uri := customTypeURI[kind]; uri != "" {
			info.Schema.Custom = uri
		}
	} else if info.Schema.System == "" {
		info.Schema.System = f.ID
	}
	for _, o := range f.Options {
		info.Options = append(info.Options, model.FieldOption{ID: o.ID, Value: o.Value})
	}
	for i, existing := range s.fields {
		if existing.ID == f.ID {
			s.fields[i] = info
			return
		}
	}
	s.fields = append(s.fields, info)
}

func fixtureFieldsFromStore(fields []model.FieldInfo) []fixtures.Field {
	var out []fixtures.Field
	for _, f := range fields {
		if !f.Custom {
			continue
		}
		ff := fixtures.Field{
			ID: f.ID, Name: f.Name, Custom: true,
			Type: fieldKind(f),
		}
		for _, o := range f.Options {
			ff.Options = append(ff.Options, fixtures.FieldOption{ID: o.ID, Value: o.Value})
		}
		out = append(out, ff)
	}
	return out
}

func (s *Store) fieldByIDLocked(id string) *model.FieldInfo {
	for i := range s.fields {
		if s.fields[i].ID == id {
			cp := s.fields[i]
			return &cp
		}
	}
	return nil
}

func (s *Store) validateCustomWriteLocked(id string, v any) error {
	f := s.fieldByIDLocked(id)
	if f == nil || !f.Custom {
		return nil
	}
	switch fieldKind(*f) {
	case KindOption:
		return validateOptionValue(id, v, f.Options, false)
	case KindOptionArray:
		return validateOptionValue(id, v, f.Options, true)
	case KindText:
		if v == nil {
			return nil
		}
		if _, ok := v.(string); !ok {
			return FieldError{Field: id, Msg: "must be a string"}
		}
	case KindNumber:
		if v == nil {
			return nil
		}
		switch v.(type) {
		case float64, float32, int, int32, int64, json.Number:
			return nil
		default:
			return FieldError{Field: id, Msg: "must be a number"}
		}
	case KindDate:
		if v == nil {
			return nil
		}
		ds, ok := v.(string)
		if !ok {
			return FieldError{Field: id, Msg: "must be YYYY-MM-DD"}
		}
		if ds == "" {
			return nil
		}
		if err := validateDateOnly(ds); err != nil {
			return FieldError{Field: id, Msg: err.Error()}
		}
	}
	return nil
}

func validateOptionValue(field string, v any, opts []model.FieldOption, multi bool) error {
	allowed := map[string]bool{}
	byValue := map[string]string{}
	for _, o := range opts {
		allowed[o.ID] = true
		if o.Value != "" {
			byValue[o.Value] = o.ID
		}
	}
	if v == nil {
		return nil
	}
	if multi {
		switch t := v.(type) {
		case []any:
			for _, item := range t {
				if err := checkOneOption(field, item, allowed, byValue); err != nil {
					return err
				}
			}
			return nil
		case []string:
			for _, id := range t {
				if err := checkOneOption(field, id, allowed, byValue); err != nil {
					return err
				}
			}
			return nil
		default:
			return FieldError{Field: field, Msg: "must be an array of options"}
		}
	}
	return checkOneOption(field, v, allowed, byValue)
}

func checkOneOption(field string, v any, allowed map[string]bool, byValue map[string]string) error {
	switch t := v.(type) {
	case string:
		if allowed[t] || byValue[t] != "" {
			return nil
		}
		return FieldError{Field: field, Msg: "option id " + t + " is not allowed"}
	case map[string]any:
		if id, ok := t["id"].(string); ok && id != "" {
			if !allowed[id] {
				return FieldError{Field: field, Msg: "option id " + id + " is not allowed"}
			}
			return nil
		}
		if val, ok := t["value"].(string); ok && byValue[val] != "" {
			return nil
		}
		return FieldError{Field: field, Msg: "option must have a valid id"}
	default:
		return FieldError{Field: field, Msg: "option must be {id} or a string id"}
	}
}

func validateDateOnly(s string) error {
	t, err := time.Parse("2006-01-02", s)
	if err != nil || t.Format("2006-01-02") != s {
		return fmt.Errorf("must be YYYY-MM-DD")
	}
	return nil
}

func setDueDate(iss *model.Issue, v any) error {
	if v == nil {
		iss.Duedate = ""
		return nil
	}
	ds, ok := v.(string)
	if !ok {
		return FieldError{Field: "duedate", Msg: "must be YYYY-MM-DD"}
	}
	if ds == "" {
		iss.Duedate = ""
		return nil
	}
	if err := validateDateOnly(ds); err != nil {
		return FieldError{Field: "duedate", Msg: err.Error()}
	}
	iss.Duedate = ds
	return nil
}

func promoteDueDateFromCustom(iss *model.Issue) {
	if iss.Custom == nil {
		return
	}
	raw, ok := iss.Custom["duedate"]
	if !ok {
		return
	}
	if iss.Duedate == "" {
		if ds, ok := raw.(string); ok {
			iss.Duedate = ds
		}
	}
	delete(iss.Custom, "duedate")
	if len(iss.Custom) == 0 {
		iss.Custom = nil
	}
}

// FieldRegistryRow is the admin/debug view of the field registry.
type FieldRegistryRow struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Kind    string              `json:"kind"`
	Custom  bool                `json:"custom"`
	Options []model.FieldOption `json:"options,omitempty"`
}

// FieldRegistry is the lab probe for "what is editable and which values
// are allowed". Served on GET /api/data.
func (s *Store) FieldRegistry() []FieldRegistryRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FieldRegistryRow, 0, len(s.fields))
	for _, f := range s.fields {
		kind := fieldKind(f)
		if !f.Custom {
			kind = f.Schema.System
			if kind == "" {
				kind = f.Schema.Type
			}
		}
		row := FieldRegistryRow{ID: f.ID, Name: f.Name, Kind: kind, Custom: f.Custom}
		if len(f.Options) > 0 {
			row.Options = append([]model.FieldOption{}, f.Options...)
		}
		out = append(out, row)
	}
	return out
}
