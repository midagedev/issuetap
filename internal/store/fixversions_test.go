package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

// GDK-516: UpdateIssue had no fixVersions/components cases, so fields.*
// landed in Custom (GET overlay looked fine; typed arrays and JQL stayed
// stale) and update.* was "unsupported update field".

func tap1Named(t *testing.T, st *Store, field string) model.Named {
	t.Helper()
	iss := st.Issue("TAP-1")
	if iss == nil {
		t.Fatal("TAP-1 missing")
	}
	var list []model.Named
	switch field {
	case "fixVersions":
		list = iss.FixVersions
	case "components":
		list = iss.Components
	}
	if len(list) == 0 {
		t.Fatalf("TAP-1 missing %s", field)
	}
	return list[0]
}

func searchKeys(t *testing.T, st *Store, jqlText string) []string {
	t.Helper()
	issues, _, err := st.Search(jqlText, 0, -1)
	if err != nil {
		t.Fatalf("search %q: %v", jqlText, err)
	}
	out := make([]string, 0, len(issues))
	for _, iss := range issues {
		out = append(out, iss.Key)
	}
	return out
}

func hasKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestUpdateIssueFixVersionsAddByID(t *testing.T) {
	st := loadTiny(t)
	v := tap1Named(t, st, "fixVersions")
	err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"fixVersions": []any{
			map[string]any{"add": map[string]any{"id": v.ID}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if iss == nil {
		t.Fatal("TAP-2 missing")
	}
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatalf("fixVersions stored in Custom (%v); want typed Issue.FixVersions", iss.Custom["fixVersions"])
	}
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].ID != v.ID || iss.FixVersions[0].Name != v.Name {
		t.Fatalf("FixVersions=%+v, want [{ID:%s Name:%s}]", iss.FixVersions, v.ID, v.Name)
	}
	keys := searchKeys(t, st, `fixVersion = "`+v.Name+`"`)
	if !hasKey(keys, "TAP-2") {
		t.Fatalf("JQL fixVersion = %q returned %v, want TAP-2", v.Name, keys)
	}
}

func TestUpdateIssueFixVersionsFieldsReplace(t *testing.T) {
	st := loadTiny(t)
	v := tap1Named(t, st, "fixVersions")
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"fixVersions": []any{map[string]any{"name": v.Name}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatalf("fields.fixVersions stored in Custom (%v)", iss.Custom["fixVersions"])
	}
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].Name != v.Name {
		t.Fatalf("FixVersions=%+v, want name %s", iss.FixVersions, v.Name)
	}
	keys := searchKeys(t, st, `fixVersion = "`+v.Name+`"`)
	if !hasKey(keys, "TAP-2") {
		t.Fatalf("JQL after fields replace returned %v, want TAP-2", keys)
	}
}

func TestUpdateIssueComponentsAddByName(t *testing.T) {
	st := loadTiny(t)
	c := tap1Named(t, st, "components")
	err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"components": []any{
			map[string]any{"add": map[string]any{"name": c.Name}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if _, inCustom := iss.Custom["components"]; inCustom {
		t.Fatalf("components stored in Custom (%v)", iss.Custom["components"])
	}
	if len(iss.Components) != 1 || iss.Components[0].Name != c.Name {
		t.Fatalf("Components=%+v, want name %s", iss.Components, c.Name)
	}
	keys := searchKeys(t, st, `component = "`+c.Name+`"`)
	if !hasKey(keys, "TAP-2") {
		t.Fatalf("JQL component = %q returned %v, want TAP-2", c.Name, keys)
	}
}

func TestUpdateIssueComponentsFieldsReplace(t *testing.T) {
	st := loadTiny(t)
	c := tap1Named(t, st, "components")
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"components": []any{map[string]any{"id": c.ID}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if _, inCustom := iss.Custom["components"]; inCustom {
		t.Fatalf("fields.components stored in Custom (%v)", iss.Custom["components"])
	}
	if len(iss.Components) != 1 || iss.Components[0].ID != c.ID || iss.Components[0].Name != c.Name {
		t.Fatalf("Components=%+v, want %+v", iss.Components, c)
	}
	keys := searchKeys(t, st, `component = "`+c.Name+`"`)
	if !hasKey(keys, "TAP-2") {
		t.Fatalf("JQL after fields replace returned %v, want TAP-2", keys)
	}
}

func TestUpdateIssueFixVersionsUnknownID(t *testing.T) {
	st := loadTiny(t)
	before := append([]model.Named{}, st.Issue("TAP-2").FixVersions...)
	err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"fixVersions": []any{
			map[string]any{"add": map[string]any{"id": "99999"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown fixVersions id")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "fixVersions" {
		t.Fatalf("want FieldError{Field:fixVersions}, got %T %v", err, err)
	}
	iss := st.Issue("TAP-2")
	if len(iss.FixVersions) != len(before) {
		t.Fatalf("rejected write mutated FixVersions=%+v", iss.FixVersions)
	}
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatal("rejected write still landed in Custom")
	}
}

func TestUpdateIssueComponentsUnknownID(t *testing.T) {
	st := loadTiny(t)
	err := st.UpdateIssue("TAP-2", map[string]any{
		"components": []any{map[string]any{"id": "99999"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown components id")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "components" {
		t.Fatalf("want FieldError{Field:components}, got %T %v", err, err)
	}
}

func TestUpdateIssueUnknownSystemKeyStillCustom(t *testing.T) {
	st := loadTiny(t)
	before := append([]model.Named{}, st.Issue("TAP-2").Versions...)
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"versions": []any{map[string]any{"name": "should-stay-custom"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if iss.Custom["versions"] == nil {
		t.Fatal("unknown system key versions was not stored in Custom")
	}
	if len(iss.Versions) != len(before) {
		t.Fatalf("typed Versions mutated (%+v); this round only promotes fixVersions and components", iss.Versions)
	}
}

func TestUpdateIssueFixVersionsClearsCustomOverlay(t *testing.T) {
	st := loadTiny(t)
	v := tap1Named(t, st, "fixVersions")
	iss := st.Issue("TAP-2")
	iss.Custom = map[string]any{"fixVersions": []any{map[string]any{"name": "stale"}}}
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"fixVersions": []any{map[string]any{"name": v.Name}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	iss = st.Issue("TAP-2")
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatalf("Custom overlay left in place: %v", iss.Custom["fixVersions"])
	}
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].Name != v.Name {
		t.Fatalf("FixVersions=%+v", iss.FixVersions)
	}
}

func TestUpdateIssueFixVersionsEmptyCatalogAllowsName(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(fixtures.Doc{
		Projects: []fixtures.Project{{Key: "ZED", Name: "Zed"}},
		Issues:   []fixtures.Issue{{Key: "ZED-1", Summary: "blank", Project: "ZED"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateIssue("ZED-1", map[string]any{
		"fixVersions": []any{map[string]any{"name": "v-lab"}},
	}, nil); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("ZED-1")
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].Name != "v-lab" {
		t.Fatalf("empty-catalog name write: %+v", iss.FixVersions)
	}
	if iss.FixVersions[0].ID == "" {
		t.Fatal("empty-catalog write stored a name with no id")
	}
}

func TestUpdateIssueFixVersionsPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	v := tap1Named(t, st, "fixVersions")
	if err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"fixVersions": []any{
			map[string]any{"add": map[string]any{"id": v.ID}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persist file: %v", err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	iss := st2.Issue("TAP-2")
	if iss == nil {
		t.Fatal("TAP-2 lost across restart")
	}
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].Name != v.Name {
		t.Fatalf("reloaded FixVersions=%+v, want name %s", iss.FixVersions, v.Name)
	}
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatalf("reloaded Custom still has fixVersions: %v", iss.Custom["fixVersions"])
	}
	keys := searchKeys(t, st2, `fixVersion = "`+v.Name+`"`)
	if !hasKey(keys, "TAP-2") {
		t.Fatalf("JQL after persist returned %v, want TAP-2", keys)
	}
}

func TestUpdateIssueComponentsRemove(t *testing.T) {
	st := loadTiny(t)
	c := tap1Named(t, st, "components")
	if err := st.UpdateIssue("TAP-1", nil, map[string]any{
		"components": []any{
			map[string]any{"remove": map[string]any{"name": c.Name}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if len(iss.Components) != 0 {
		t.Fatalf("after remove Components=%+v", iss.Components)
	}
	keys := searchKeys(t, st, `component = "`+c.Name+`"`)
	if hasKey(keys, "TAP-1") {
		t.Fatalf("JQL still returned TAP-1 after remove: %v", keys)
	}
}

// GDK-581: CreateIssue used to drop fields.fixVersions / fields.components
// into Custom, so GET overlay looked populated but JQL stayed empty.
func TestCreateIssueFixVersionsComponentsTypedThenJQL(t *testing.T) {
	st := loadTiny(t)
	v := tap1Named(t, st, "fixVersions")
	c := tap1Named(t, st, "components")
	iss, err := st.CreateIssue(map[string]any{
		"project":     map[string]any{"key": "TAP"},
		"summary":     "create named lists",
		"issuetype":   map[string]any{"id": "10003"},
		"fixVersions": []any{map[string]any{"id": v.ID}},
		"components":  []any{map[string]any{"name": c.Name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, inCustom := iss.Custom["fixVersions"]; inCustom {
		t.Fatalf("fixVersions stored in Custom (%v)", iss.Custom["fixVersions"])
	}
	if _, inCustom := iss.Custom["components"]; inCustom {
		t.Fatalf("components stored in Custom (%v)", iss.Custom["components"])
	}
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].ID != v.ID || iss.FixVersions[0].Name != v.Name {
		t.Fatalf("FixVersions=%+v, want [{ID:%s Name:%s}]", iss.FixVersions, v.ID, v.Name)
	}
	if len(iss.Components) != 1 || iss.Components[0].ID != c.ID || iss.Components[0].Name != c.Name {
		t.Fatalf("Components=%+v, want [{ID:%s Name:%s}]", iss.Components, c.ID, c.Name)
	}
	if !hasKey(searchKeys(t, st, `fixVersion = "`+v.Name+`"`), iss.Key) {
		t.Fatalf("JQL fixVersion missed created issue %s", iss.Key)
	}
	if !hasKey(searchKeys(t, st, `component = "`+c.Name+`"`), iss.Key) {
		t.Fatalf("JQL component missed created issue %s", iss.Key)
	}
}

// Create shares resolveNamedListLocked with PUT, but the create surface pins
// its own rejection: an unknown id must refuse the create, not 201 into
// Custom (the plausible-lie class GDK-581 closed).
func TestCreateIssueFixVersionsUnknownID(t *testing.T) {
	st := loadTiny(t)
	_, err := st.CreateIssue(map[string]any{
		"project":     map[string]any{"key": "TAP"},
		"summary":     "unknown fix version",
		"issuetype":   map[string]any{"id": "10003"},
		"fixVersions": []any{map[string]any{"id": "99999"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown fixVersions id on create")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "fixVersions" {
		t.Fatalf("want FieldError{Field:fixVersions}, got %T %v", err, err)
	}
}
