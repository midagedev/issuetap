package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/locale"
)

// GDK-662: GET /project/{key}/versions|components is the issue-derived
// catalog. Map iteration is non-deterministic; public methods must sort.

func TestProjectVersionsSortedDeterministic(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if _, err := st.CreateProject("LAB", "Lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "LAB"},
		"summary": "named versions",
		"fixVersions": []any{
			map[string]any{"name": "zeta"},
			map[string]any{"name": "alpha"},
			map[string]any{"id": "b", "name": "same"},
			map[string]any{"id": "a", "name": "same"},
		},
	}, ""); err != nil {
		t.Fatal(err)
	}
	first := st.ProjectVersions("LAB")
	second := st.ProjectVersions("LAB")
	if len(first) != 4 {
		t.Fatalf("len=%d want 4: %+v", len(first), first)
	}
	want := []string{"alpha", "same", "same", "zeta"}
	wantID := []string{"", "a", "b", ""}
	for i, n := range first {
		if n.Name != want[i] {
			t.Fatalf("order[%d].Name=%q want %q in %+v", i, n.Name, want[i], first)
		}
		if wantID[i] != "" && n.ID != wantID[i] {
			t.Fatalf("order[%d].ID=%q want %q in %+v", i, n.ID, wantID[i], first)
		}
		if second[i] != n {
			t.Fatalf("second call diverged at %d: %+v vs %+v", i, second, first)
		}
	}
}

func TestProjectComponentsSortedDeterministic(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if _, err := st.CreateProject("LAB", "Lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "LAB"},
		"summary": "named components",
		"components": []any{
			map[string]any{"name": "zeta"},
			map[string]any{"name": "alpha"},
		},
	}, ""); err != nil {
		t.Fatal(err)
	}
	first := st.ProjectComponents("LAB")
	second := st.ProjectComponents("LAB")
	if len(first) != 2 || first[0].Name != "alpha" || first[1].Name != "zeta" {
		t.Fatalf("order=%+v want alpha, zeta", first)
	}
	if first[0] != second[0] || first[1] != second[1] {
		t.Fatalf("second call diverged: %+v vs %+v", second, first)
	}
}

func TestProjectVersionsFiltersByProject(t *testing.T) {
	st := loadTiny(t)
	if _, err := st.CreateProject("ZZZ", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateIssue(map[string]any{
		"project":     map[string]any{"key": "ZZZ"},
		"summary":     "other project version",
		"fixVersions": []any{map[string]any{"name": "zzz-only"}},
		"components":  []any{map[string]any{"name": "zzz-comp"}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	for _, n := range st.ProjectVersions("TAP") {
		if n.Name == "zzz-only" {
			t.Fatalf("TAP versions leaked ZZZ: %+v", st.ProjectVersions("TAP"))
		}
	}
	for _, n := range st.ProjectComponents("TAP") {
		if n.Name == "zzz-comp" {
			t.Fatalf("TAP components leaked ZZZ: %+v", st.ProjectComponents("TAP"))
		}
	}
	gotV := st.ProjectVersions("ZZZ")
	if len(gotV) != 1 || gotV[0].Name != "zzz-only" {
		t.Fatalf("ZZZ versions=%+v", gotV)
	}
	gotC := st.ProjectComponents("ZZZ")
	if len(gotC) != 1 || gotC[0].Name != "zzz-comp" {
		t.Fatalf("ZZZ components=%+v", gotC)
	}
	empty := st.ProjectVersions("NOSUCH")
	if empty == nil || len(empty) != 0 {
		t.Fatalf("missing project versions=%#v want empty slice", empty)
	}
}
