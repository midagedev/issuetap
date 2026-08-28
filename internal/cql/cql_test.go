package cql

import (
	"testing"

	"github.com/midagedev/issuetap/internal/model"
)

func TestParseSingleSpace(t *testing.T) {
	q, err := Parse(`space="LOC" AND type=page AND lastModified >= "2026-01-02 15:04" order by lastmodified asc`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Spaces) != 1 || q.Spaces[0] != "LOC" {
		t.Fatalf("Spaces = %v, want [LOC]", q.Spaces)
	}
	if q.Type != "page" || !q.HasAfter {
		t.Fatalf("Type=%q HasAfter=%v", q.Type, q.HasAfter)
	}
}

// space IN is what gadak's chunked incremental sync sends (gadak GDK-1074).
// Before this clause existed, Parse returned `cql: not a predicate` here —
// a chunked client against an older server fails loudly, never silently.
func TestParseSpaceIn(t *testing.T) {
	q, err := Parse(`space IN ("LOC", "ENG") AND type=comment AND lastModified >= "2026-01-02 15:04" order by lastmodified asc`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Spaces) != 2 || q.Spaces[0] != "LOC" || q.Spaces[1] != "ENG" {
		t.Fatalf("Spaces = %v, want [LOC ENG]", q.Spaces)
	}
	if q.Type != "comment" {
		t.Fatalf("Type = %q, want comment", q.Type)
	}

	// Lower-case keyword and unquoted keys parse too.
	q, err = Parse(`space in (LOC,ENG) AND type=page`)
	if err != nil {
		t.Fatalf("Parse lower-case in: %v", err)
	}
	if len(q.Spaces) != 2 {
		t.Fatalf("Spaces = %v, want two keys", q.Spaces)
	}
}

func TestParseSpaceInMalformedIsError(t *testing.T) {
	for _, raw := range []string{
		`space IN () AND type=page`,
		`space IN "LOC" AND type=page`,
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q): want error, got nil", raw)
		}
	}
}

func TestMatchSpaceSet(t *testing.T) {
	q, err := Parse(`space IN ("AAA","BBB") AND type=page`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !MatchPage(q, &model.Page{SpaceKey: "aaa", Type: "page"}) {
		t.Error("member space (case-folded) should match")
	}
	if MatchPage(q, &model.Page{SpaceKey: "CCC", Type: "page"}) {
		t.Error("non-member space should not match")
	}
	if !MatchComment(q, "BBB", "2026-01-02T00:00:00.000Z") {
		// Untyped space filter applies to comments through the same set.
		q2, _ := Parse(`space IN ("AAA","BBB") AND type=comment`)
		if !MatchComment(q2, "BBB", "2026-01-02T00:00:00.000Z") {
			t.Error("member space comment should match")
		}
	}
}
