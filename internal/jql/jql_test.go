package jql

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"

	"github.com/midagedev/issuetap/internal/model"
)

func TestParseOrderOnly(t *testing.T) {
	q, err := Parse("ORDER BY updated DESC")
	if err != nil {
		t.Fatal(err)
	}
	if q.Root != nil {
		t.Fatal("expected match-all")
	}
	if len(q.Order) != 1 || q.Order[0].Field != "updated" || !q.Order[0].Desc {
		t.Fatalf("order=%v", q.Order)
	}
}

func TestParseProjectAndUpdated(t *testing.T) {
	q, err := Parse(`project in ("TAP", "OPS") AND updated >= "2026/08/01 10:00" ORDER BY updated ASC`)
	if err != nil {
		t.Fatal(err)
	}
	if q.Root == nil {
		t.Fatal("nil root")
	}
}

func TestParseBad(t *testing.T) {
	if _, err := Parse("%%%"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFilterKey(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Updated: "2026-08-02T10:00:00.000+0900"},
		{Key: "OPS-1", ProjectKey: "OPS", Updated: "2026-08-03T10:00:00.000+0900"},
	}
	q, err := Parse(`key = "TAP-1"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("%v", got)
	}
}

func TestFilterProjectIn(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Updated: "2026-08-02T10:00:00.000+0900"},
		{Key: "OPS-1", ProjectKey: "OPS", Updated: "2026-08-03T10:00:00.000+0900"},
	}
	q, err := Parse(`project in ("OPS")`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "OPS-1" {
		t.Fatalf("%v", got)
	}
}

func TestFilterFixVersion(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", FixVersions: []model.Named{{ID: "v1", Name: "2026.8"}}},
		{Key: "TAP-2", ProjectKey: "TAP"},
	}
	q, err := Parse(`fixVersion = "2026.8"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("fixVersion by name: %v", got)
	}
	q, err = Parse(`fixVersion = v1`)
	if err != nil {
		t.Fatal(err)
	}
	got = Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("fixVersion by id: %v", got)
	}
}

func TestFilterComponent(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Components: []model.Named{{ID: "c1", Name: "Core"}}},
		{Key: "TAP-2", ProjectKey: "TAP"},
	}
	q, err := Parse(`component = "Core"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("%v", got)
	}
}

// gadak GDK-1209: an unknown field used to evaluate to nil values — `=`
// matched nothing (silent 0 rows), `!=` matched everything — and an
// unknown ORDER BY field silently fell back to key order. Both are now
// honest errors, the CQL pattern.
func TestParseUnknownFieldRejected(t *testing.T) {
	for _, raw := range []string{
		`sprint = 5`,
		`resolution != Done AND project = TAP`,
		`project = TAP OR epic in (A, B)`,
		`text ~ "foo"`,
	} {
		_, err := Parse(raw)
		if err == nil {
			t.Errorf("Parse(%q) accepted an unknown field", raw)
			continue
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Errorf("Parse(%q) error %q, want the Cloud unknown-field message", raw, err)
		}
	}
}

func TestParseKnownFieldsStillAccepted(t *testing.T) {
	for _, raw := range []string{
		`project = TAP AND status != Done ORDER BY updated DESC`,
		`statusCategory = indeterminate AND type = Bug`,
		`fixVersion = "2026.8" OR component = Core`,
		`summary = x AND labels in (a, b) ORDER BY created ASC, key DESC`,
	} {
		if _, err := Parse(raw); err != nil {
			t.Errorf("Parse(%q): %v", raw, err)
		}
	}
}

func TestParseUnknownOrderByRejected(t *testing.T) {
	for _, raw := range []string{
		"ORDER BY rank DESC",
		"project = TAP ORDER BY priority",
	} {
		_, err := Parse(raw)
		if err == nil {
			t.Errorf("Parse(%q) accepted an unsupported sort field", raw)
			continue
		}
		if !strings.Contains(err.Error(), "sort") {
			t.Errorf("Parse(%q) error %q, want a sort-field message", raw, err)
		}
	}
}

// Every stored filter a fixture ships must survive the field whitelist —
// otherwise GET /filter/my hands out JQL whose execution is a 400
// (gadak GDK-1209's "saved but unrunnable" shape).
func TestExampleFixtureFilterJQLParses(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(fixtures.RepoRoot(), "examples", "fixtures", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(paths))
	}
	for _, p := range paths {
		doc, err := fixtures.Load(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, f := range doc.Filters {
			if _, err := Parse(f.JQL); err != nil {
				t.Errorf("%s filter %q: %v", filepath.Base(p), f.JQL, err)
			}
		}
	}
}
