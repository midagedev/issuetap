package diagnostics

import (
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
)

func TestDiagnoseReportsInvalidParentLinks(t *testing.T) {
	in := Input{
		Counts: map[string]int{"issues": 2},
		Snapshot: fixtures.Doc{
			IssueTypes: []fixtures.IssueType{
				{ID: "10000", Name: "Epic", HierarchyLevel: 1},
				{ID: "10003", Name: "Task", HierarchyLevel: 0},
			},
			Issues: []fixtures.Issue{
				{Key: "TAP-1", Type: "10003", Summary: "parent task"},
				{Key: "TAP-2", Type: "10003", Summary: "child task", Parent: "TAP-1"},
			},
		},
	}
	out := diagnose(in)
	causes, _ := out["likelyCauses"].([]string)
	found := false
	for _, c := range causes {
		if strings.Contains(c, "1") && strings.Contains(c, "parent") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a parent-hierarchy cause naming the count, got %v", causes)
	}
}

func TestDiagnoseSilentWhenParentsLegal(t *testing.T) {
	in := Input{
		Counts: map[string]int{"issues": 2},
		Snapshot: fixtures.Doc{
			IssueTypes: []fixtures.IssueType{
				{ID: "10000", Name: "Epic", HierarchyLevel: 1},
				{ID: "10003", Name: "Task", HierarchyLevel: 0},
			},
			Issues: []fixtures.Issue{
				{Key: "TAP-100", Type: "10000", Summary: "epic"},
				{Key: "TAP-1", Type: "10003", Summary: "task", Parent: "TAP-100"},
			},
		},
	}
	out := diagnose(in)
	causes, _ := out["likelyCauses"].([]string)
	for _, c := range causes {
		if strings.Contains(c, "parent") && strings.Contains(c, "hierarchy") {
			t.Fatalf("legal parent reported as a diagnosis: %v", causes)
		}
	}
}
