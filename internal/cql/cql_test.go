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

// GDK-1210 ②: the ORDER BY direction must reach the Query. `desc` used to
// be peeled off with the clause and silently served ascending (OrderAsc is
// initialized true and never reassigned) — SearchPages sorts on that flag,
// so `order by lastmodified desc` answered oldest-first.
func TestParseOrderByDirection(t *testing.T) {
	q, err := Parse(`space="DOCS" AND type=page ORDER BY lastmodified DESC`)
	if err != nil {
		t.Fatalf("Parse desc: %v", err)
	}
	if q.OrderAsc {
		t.Fatal("OrderAsc=true for DESC, want false")
	}
	for _, raw := range []string{
		`space="DOCS" AND type=page order by lastmodified asc`,
		`space="DOCS" AND type=page order by lastmodified`, // no direction = ASC
	} {
		q, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if !q.OrderAsc {
			t.Fatalf("Parse(%q): OrderAsc=false, want true", raw)
		}
	}
}

// The peeled clause is not a free-text bucket. Ordering by anything but
// lastmodified has no sort implementation behind it — serving such a query
// in id order would look like it worked (the GDK-1209 silent-evaluation
// class). Same honesty rule as every other clause here: error.
func TestParseOrderByUnknownFieldIsError(t *testing.T) {
	for _, raw := range []string{
		`space="DOCS" order by title`,
		`space="DOCS" order by lastmodified nonsense`,
		`space="DOCS" order by`,
	} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q): want error, got nil", raw)
		}
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
