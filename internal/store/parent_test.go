package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func TestCreateIssueRejectsSameLevelParent(t *testing.T) {
	st := loadTiny(t)
	_, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "task under task",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-2"},
	}, "")
	assertParentFieldError(t, err, "parent", "parentId")
}

func TestCreateIssueRejectsEpicUnderEpic(t *testing.T) {
	st := loadTiny(t)
	epic, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic a",
		"issuetype": map[string]any{"id": "10000"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic under epic",
		"issuetype": map[string]any{"id": "10000"},
		"parent":    map[string]any{"key": epic.Key},
	}, "")
	assertParentFieldError(t, err, "parent", "parentId")
}

func TestCreateIssueAcceptsEpicParentOfTask(t *testing.T) {
	st := loadTiny(t)
	epic, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic parent",
		"issuetype": map[string]any{"id": "10000"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "task under epic",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": epic.Key},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ParentKey != epic.Key {
		t.Fatalf("ParentKey=%q want %q", iss.ParentKey, epic.Key)
	}
}

func TestCreateIssueAcceptsTaskParentOfSubtask(t *testing.T) {
	st := loadTiny(t)
	iss, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "sub-task under task",
		"issuetype": map[string]any{"id": "10002"},
		"parent":    map[string]any{"key": "TAP-2"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if iss.ParentKey != "TAP-2" {
		t.Fatalf("ParentKey=%q", iss.ParentKey)
	}
}

func TestUpdateIssueRejectsSameLevelParent(t *testing.T) {
	st := loadTiny(t)
	err := st.UpdateIssue("TAP-2", map[string]any{
		"parent": map[string]any{"key": "TAP-3"},
	}, nil)
	assertParentFieldError(t, err, "pid")
	if st.Issue("TAP-2").ParentKey != "" {
		t.Fatalf("rejected PUT still set ParentKey=%q", st.Issue("TAP-2").ParentKey)
	}
}

func TestCreateIssueRejectsUnknownParent(t *testing.T) {
	st := loadTiny(t)
	_, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "missing parent",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-MISSING"},
	}, "")
	assertParentFieldError(t, err, "parent", "parentId")
}

func TestParentHierarchyKeysOnTypeIDNotName(t *testing.T) {
	doc, err := fixtures.Load(fixtures.Example("korean.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st := New(Options{Seed: 1, Locale: locale.KO})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	// TAP-2 is id 10003 named 작업. Name-keyed logic looking for "Task" would miss this.
	_, err = st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "same level",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-2"},
	}, "")
	assertParentFieldError(t, err, "parent", "parentId")
}

func TestApplyKeepsInvalidParentLinks(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	doc := fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		IssueTypes: []fixtures.IssueType{
			{ID: "10000", Name: "Epic", HierarchyLevel: 1},
			{ID: "10003", Name: "Task", HierarchyLevel: 0},
		},
		Issues: []fixtures.Issue{
			{Key: "TAP-1", Summary: "parent task", Type: "10003", Project: "TAP"},
			{Key: "TAP-2", Summary: "child task", Type: "10003", Project: "TAP", Parent: "TAP-1"},
		},
	}
	if err := st.Apply(doc); err != nil {
		t.Fatalf("load of illegal parent must succeed (persist is the original): %v", err)
	}
	got := st.Issue("TAP-2")
	if got == nil || got.ParentKey != "TAP-1" {
		t.Fatalf("load rewrote parent: %+v", got)
	}
	if n := InvalidParentCount(st.Snapshot()); n != 1 {
		t.Fatalf("InvalidParentCount=%d want 1", n)
	}
}

func assertParentFieldError(t *testing.T, err error, keys ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected FieldError, got nil")
	}
	fe, ok := AsFieldError(err)
	if !ok {
		t.Fatalf("want FieldError, got %T %v", err, err)
	}
	m := fe.Map()
	for _, k := range keys {
		if m[k] == "" {
			t.Fatalf("FieldError missing key %q in %v (err=%v)", k, m, err)
		}
	}
	if len(m) != len(keys) {
		t.Fatalf("FieldError keys %v want exactly %v", m, keys)
	}
}
