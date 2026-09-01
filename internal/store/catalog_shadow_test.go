package store

// GDK-1284: a fixture inherits the seeded catalog for what it does not
// mention — every shipped example declares Task and Bug and lets Sub-task
// come from the defaults, so wholesale replacement is not the fix. What it
// must not inherit is a default answering to a name the fixture now uses
// for a different id. The measured symptom on a migrated workspace was two
// types named "Epic" (the default 10000 beside the migrated 10001) and two
// statuses named "In Progress" (the default 3 beside the migrated 10001),
// which leaves every name-keyed write with no answer.

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
)

func namesByID(t *testing.T, pairs [][2]string) map[string][]string {
	t.Helper()
	byName := map[string][]string{}
	for _, p := range pairs {
		byName[p[1]] = append(byName[p[1]], p[0])
	}
	return byName
}

func hasID(pairs [][2]string, id string) bool {
	for _, p := range pairs {
		if p[0] == id {
			return true
		}
	}
	return false
}

func TestFixtureCatalogEvictsShadowedDefaults(t *testing.T) {
	st := New(Options{Seed: 1})
	defer st.Close()

	// The shape a migrated workspace arrives in: a full catalog of its
	// own, whose ids only partly overlap the seeded defaults.
	doc := fixtures.Doc{
		IssueTypes: []fixtures.IssueType{
			// The stored name is the source site's, in its own language.
			// It is the locale overlay that renders both this and the
			// seeded 10000 as "Epic" — which is why comparing the raw
			// stored names never saw the collision.
			{ID: "10001", Name: "에픽", HierarchyLevel: 1},
			{ID: "10002", Name: "Sub-task", HierarchyLevel: -1, Subtask: true},
			{ID: "10003", Name: "Task"},
			{ID: "10007", Name: "Bug"},
		},
		Statuses: []fixtures.Status{
			{ID: "10000", Name: "To Do", Category: "new"},
			{ID: "10001", Name: "진행 중", Category: "indeterminate"},
			{ID: "10003", Name: "Done", Category: "done"},
		},
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}

	var typePairs [][2]string
	for _, ty := range st.IssueTypes() {
		typePairs = append(typePairs, [2]string{ty.ID, ty.Name})
	}
	for name, ids := range namesByID(t, typePairs) {
		if len(ids) > 1 {
			t.Errorf("issue type %q has %d ids %v — a name-keyed write cannot pick one", name, len(ids), ids)
		}
	}
	// Inheritance survives: a default the fixture never names is still
	// there. Every shipped example leans on this.
	if !hasID(typePairs, "10004") {
		t.Errorf("issue types = %v, want the unmentioned default 10004 (Story) kept", typePairs)
	}
	if !hasID(typePairs, "10001") {
		t.Errorf("issue types = %v, want the fixture's own 10001 (Epic)", typePairs)
	}

	var statusPairs [][2]string
	for _, s := range st.Statuses() {
		statusPairs = append(statusPairs, [2]string{s.ID, s.Name})
	}
	for name, ids := range namesByID(t, statusPairs) {
		if len(ids) > 1 {
			t.Errorf("status %q has %d ids %v — a name-keyed transition cannot pick one", name, len(ids), ids)
		}
	}
	if !hasID(statusPairs, "10001") {
		t.Errorf("statuses = %v, want the fixture's own 10001 (In Progress)", statusPairs)
	}
}

// A fixture that declares no catalog still gets the defaults: the store
// has to be usable with nothing but issues in the file.
func TestFixtureWithoutCatalogKeepsDefaults(t *testing.T) {
	st := New(Options{Seed: 1})
	defer st.Close()

	before := len(st.IssueTypes())
	beforeStatuses := len(st.Statuses())
	if before == 0 || beforeStatuses == 0 {
		t.Fatalf("a fresh store must seed a catalog; types=%d statuses=%d", before, beforeStatuses)
	}
	if err := st.Apply(fixtures.Doc{Projects: []fixtures.Project{{Key: "STD", Name: "Standalone"}}}); err != nil {
		t.Fatal(err)
	}
	if got := len(st.IssueTypes()); got != before {
		t.Errorf("issue types = %d after a catalog-free fixture, want the seeded %d", got, before)
	}
	if got := len(st.Statuses()); got != beforeStatuses {
		t.Errorf("statuses = %d after a catalog-free fixture, want the seeded %d", got, beforeStatuses)
	}
}
