package store

// GDK-1211 gates: create is a Cloud-contract write, not a bootstrap
// helper. Naming an unknown project on POST /issue used to create an empty
// project named after its own key — Cloud answers 400 and creates nothing.
// The issuetype default ("10003" magic number) is also catalog-derived
// here: explicit ids must exist, and an omitted type falls back through
// the catalog instead of trusting an id that GDK-1284 shadow eviction can
// remove.

import (
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
)

func rejectStore(t *testing.T) *Store {
	t.Helper()
	st := New(Options{Seed: 1})
	doc, err := fixtures.Load("../../examples/fixtures/tiny.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestCreateIssueUnknownProjectRejected: an unknown project key is a
// field error naming the project, and — the part Cloud enforces and the
// old auto-create violated — nothing is created: no project, no issue.
func TestCreateIssueUnknownProjectRejected(t *testing.T) {
	st := rejectStore(t)
	projectsBefore := len(st.Projects())

	_, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "NOPE"},
		"summary":   "must not bootstrap a project",
		"issuetype": map[string]any{"id": "10003"},
	}, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("create with unknown project: want error, got nil")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "project" {
		t.Fatalf("err = %v, want FieldError{project}", err)
	}
	if len(st.Projects()) != projectsBefore {
		t.Fatalf("Projects() %d → %d — the rejected create bootstrapped a project",
			projectsBefore, len(st.Projects()))
	}
	if st.Issue("NOPE-1") != nil {
		t.Fatal("NOPE-1 exists — the rejected create left an issue behind")
	}
}

// TestCreateIssueUnknownIssueTypeRejected: an explicit issuetype id that
// is not in the catalog is Cloud's "The issue type selected is invalid."
// — it used to be stored as-is, an id nothing can render.
func TestCreateIssueUnknownIssueTypeRejected(t *testing.T) {
	st := rejectStore(t)
	iss, err := st.CreateIssue(map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "bad type",
		"issuetype": map[string]any{"id": "99999"},
	}, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatalf("created %s under unknown type 99999 — want rejection", iss.Key)
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "issuetype" {
		t.Fatalf("err = %v, want FieldError{issuetype}", err)
	}
	if !strings.Contains(fe.Msg, "invalid") {
		t.Fatalf("msg = %q, want Cloud's 'invalid' phrasing", fe.Msg)
	}
}

// TestCreateIssueOmittedTypeFallsBackThroughCatalog: with 10003 evicted
// from the catalog (GDK-1284 shadow eviction can do exactly that), an
// issuetype-less create must still file under a real level-0 type — the
// lowest id — never dangle on an evicted id.
func TestCreateIssueOmittedTypeFallsBackThroughCatalog(t *testing.T) {
	st := rejectStore(t)
	// Simulate the migrated-workspace shape GDK-1284 produced: the catalog
	// no longer carries 10003 (the fixture and the seed catalog both
	// define it, so eviction is a store-level delete). Remaining level-0
	// types in tiny.yaml: 10004 Story, 10007 Bug → fallback is 10004, the
	// lowest id — never the evicted 10003.
	st.mu.Lock()
	st.sqlExec(`DELETE FROM issue_types WHERE id=?`, "10003")
	stillThere := st.typeByIDLocked("10003") != nil
	st.mu.Unlock()
	if stillThere {
		t.Fatal("precondition: 10003 still in catalog")
	}
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"},
		"summary": "no issuetype field at all",
	}, "5b10a2844c20165700ede21g")
	if err != nil {
		t.Fatalf("create without issuetype after 10003 eviction: %v", err)
	}
	if iss.IssueTypeID != "10004" {
		t.Fatalf("IssueTypeID = %q, want catalog fallback 10004 (lowest-id level-0 type)", iss.IssueTypeID)
	}
	if st.typeByIDLocked(iss.IssueTypeID) == nil {
		t.Fatalf("filed under %q which is not in the catalog", iss.IssueTypeID)
	}
}

// TestCreateIssueExistingImplicitProjectStillWorks: workspaces that used
// the old auto-create path have those projects in their persist file
// already — creates against them must keep working with no migration.
func TestCreateIssueExistingImplicitProjectStillWorks(t *testing.T) {
	st := rejectStore(t)
	// What the old path left behind: a bare project with key=name.
	if _, err := st.CreateProject("LEG", "LEG"); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "LEG"},
		"summary": "pre-existing project is not a stranger",
	}, "5b10a2844c20165700ede21g")
	if err != nil {
		t.Fatalf("create into pre-existing project: %v", err)
	}
	if !strings.HasPrefix(iss.Key, "LEG-") {
		t.Fatalf("key = %q, want LEG-n", iss.Key)
	}
}
