package store

// GDK-1219 gates. The issue's premise (mixed PUT leaves earlier fields
// applied when a later field 400s) does not reproduce on this tree — the
// store copies the issue out of SQLite and an error return discards the
// copy, so partial mutation was already impossible. That atomicity is
// *accidental* though: nothing pins it, and the genuinely reproducing
// siblings of the class were silent catalog violations — PUT with an
// unknown issuetype or priority id applied the dangling id without a
// word, where Cloud answers 400. These gates pin the atomicity (so the
// accident becomes a contract) and close the silent-apply axes, and the
// fix makes the validate-before-apply order structural instead of
// accidental.

import (
	"strings"
	"testing"
)

func mustUpdateIssue(t *testing.T, st *Store, key string, fields, update map[string]any) {
	t.Helper()
	if err := st.UpdateIssue(key, fields, update, "5b10a2844c20165700ede21g"); err != nil {
		t.Fatalf("UpdateIssue(%s): %v", key, err)
	}
}

// TestUpdateIssueRejectsUnknownIssueTypeID: a type id that is not in the
// catalog used to be stored as-is — an id nothing can render, with a
// changelog entry pointing at it. Cloud answers 400
// "The issue type selected is invalid."
func TestUpdateIssueRejectsUnknownIssueTypeID(t *testing.T) {
	st := rejectStore(t)
	before := st.Issue("TAP-2")
	histBefore := len(before.Histories)

	err := st.UpdateIssue("TAP-2", map[string]any{
		"issuetype": map[string]any{"id": "99999"},
	}, nil, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("PUT unknown issuetype 99999: want error, got nil")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "issuetype" {
		t.Fatalf("err = %v, want FieldError{issuetype}", err)
	}
	after := st.Issue("TAP-2")
	if after.IssueTypeID != before.IssueTypeID {
		t.Fatalf("IssueTypeID %q → %q — rejected write mutated the issue",
			before.IssueTypeID, after.IssueTypeID)
	}
	if len(after.Histories) != histBefore {
		t.Fatalf("histories %d → %d — rejected write left a changelog row",
			histBefore, len(after.Histories))
	}
}

// TestUpdateIssueRejectsUnknownPriorityID: same class as issuetype — a
// priority id outside the catalog was stored silently.
func TestUpdateIssueRejectsUnknownPriorityID(t *testing.T) {
	st := rejectStore(t)
	before := st.Issue("TAP-2")

	err := st.UpdateIssue("TAP-2", map[string]any{
		"priority": map[string]any{"id": "88888"},
	}, nil, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("PUT unknown priority 88888: want error, got nil")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "priority" {
		t.Fatalf("err = %v, want FieldError{priority}", err)
	}
	if after := st.Issue("TAP-2"); after.PriorityID != before.PriorityID {
		t.Fatalf("PriorityID %q → %q — rejected write mutated the issue",
			before.PriorityID, after.PriorityID)
	}
}

// TestUpdateIssueMixedFailureAppliesNothing: the GDK-1219 scenario itself
// — a PUT whose summary is fine and whose fixVersions is garbage must
// apply nothing and record nothing (all-or-nothing, Cloud semantics).
// This pins atomicity as a contract so a future refactor that starts
// writing mid-validation fails here first.
func TestUpdateIssueMixedFailureAppliesNothing(t *testing.T) {
	st := rejectStore(t)
	before := st.Issue("TAP-2")
	if before.Summary == "CHANGED SUMMARY" {
		t.Fatal("fixture surprise: TAP-2 already has the probe summary")
	}

	err := st.UpdateIssue("TAP-2", map[string]any{
		"summary":     "CHANGED SUMMARY",
		"fixVersions": []any{map[string]any{"id": "nonexistent-id"}},
	}, nil, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("mixed PUT with bad fixVersions: want error, got nil")
	}
	after := st.Issue("TAP-2")
	if after.Summary != before.Summary {
		t.Fatalf("summary %q → %q — earlier field survived a failed PUT",
			before.Summary, after.Summary)
	}
	if len(after.FixVersions) != len(before.FixVersions) {
		t.Fatalf("fixVersions %v → %v — failed PUT changed the list",
			before.FixVersions, after.FixVersions)
	}
	if after.Updated != before.Updated {
		t.Fatalf("Updated %q → %q — failed PUT bumped the timestamp",
			before.Updated, after.Updated)
	}
	if len(after.Histories) != len(before.Histories) {
		t.Fatalf("histories %d → %d — failed PUT recorded a changelog row",
			len(before.Histories), len(after.Histories))
	}
}

// TestUpdateIssueMixedUpdateOpFailureAppliesNothing: same pin for the
// update-op shape — a good fields write plus a bad update op leaves both
// untouched.
func TestUpdateIssueMixedUpdateOpFailureAppliesNothing(t *testing.T) {
	st := rejectStore(t)
	before := st.Issue("TAP-2")

	err := st.UpdateIssue("TAP-2", map[string]any{"summary": "SECOND CHANGE"}, map[string]any{
		"components": []any{map[string]any{"add": map[string]any{"id": "nope"}}},
	}, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("mixed PUT with bad update op: want error, got nil")
	}
	after := st.Issue("TAP-2")
	if after.Summary != before.Summary {
		t.Fatalf("summary %q → %q — fields half of a failed PUT applied",
			before.Summary, after.Summary)
	}
	if len(after.Labels) != len(before.Labels) || len(after.Components) != len(before.Components) {
		t.Fatalf("labels/components drifted on a failed PUT")
	}
}

// TestUpdateIssueFirstErrorIsDeterministic: the same invalid request must
// always fail on the same field. With more than one erroring field the
// map iteration order used to pick the reported field at random — two
// clients sending the identical PUT could see different errors.
func TestUpdateIssueFirstErrorIsDeterministic(t *testing.T) {
	first := ""
	for i := 0; i < 60; i++ {
		st := rejectStore(t)
		err := st.UpdateIssue("TAP-2", map[string]any{
			"summary":     "never applied",
			"issuetype":   map[string]any{"id": "99999"},
			"priority":    map[string]any{"id": "88888"},
			"fixVersions": []any{map[string]any{"id": "nonexistent-id"}},
			"components":  []any{map[string]any{"id": "no-such-component"}},
		}, nil, "5b10a2844c20165700ede21g")
		if err == nil {
			t.Fatal("all-invalid PUT: want error, got nil")
		}
		msg := err.Error()
		if first == "" {
			first = msg
			continue
		}
		if msg != first {
			t.Fatalf("identical PUT reported different first errors:\n  %s\n  %s", first, msg)
		}
	}
	if !strings.Contains(first, "components") {
		t.Fatalf("first error = %q — sorted key order should report components first", first)
	}
}

// TestUpdateIssueUnknownFieldInUpdateMapIsError: an update key with no
// implementation must never be a silent no-op (existing contract, pinned
// here because it is the same honesty rule the new validations follow).
func TestUpdateIssueUnknownFieldInUpdateMapIsError(t *testing.T) {
	st := rejectStore(t)
	err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"customfield_99999": []any{map[string]any{"set": "x"}},
	}, "5b10a2844c20165700ede21g")
	if err == nil {
		t.Fatal("unsupported update field: want error, got nil")
	}
}
