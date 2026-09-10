package store

// GDK-1522 (GDK-1356 remedy 1): a cutover could seed the default catalog
// (10000/3/10003) beside a migrated one, leaving two ids answering one
// name — "In Progress" as both 3 and 10001 — so every name-keyed write and
// every catalog listing was ambiguous. MergeStatus is the cleanup verb.
// The duplicate shape is built straight into the working copy because
// Apply now evicts same-name fixture rows (GDK-1284): the only places
// these rows still exist are persist files written before that fix,
// exactly the files this verb exists to repair.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

// dupCatalogDoc seeds issues on both duplicate ids: TAP-1 lives on the
// doomed 3 (and gets a synthesized created→3 history row), TAP-2 already
// sits on the survivor 10001, TAP-3 is elsewhere but crossed 3 in an
// authored changelog item.
func dupCatalogDoc() fixtures.Doc {
	return fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Statuses: []fixtures.Status{
			{ID: "10000", Name: "To Do", Category: "new"},
			{ID: "10003", Name: "Done", Category: "done"},
		},
		Issues: []fixtures.Issue{
			{Key: "TAP-1", Summary: "on the doomed id", Status: "3",
				Created: "2026-09-01T09:00:00.000+0900", Updated: "2026-09-01T09:00:00.000+0900"},
			{Key: "TAP-2", Summary: "already on the survivor", Status: "10001",
				Created: "2026-09-01T09:00:00.000+0900", Updated: "2026-09-01T09:00:00.000+0900"},
			{Key: "TAP-3", Summary: "crossed the doomed id in history", Status: "10000",
				Created: "2026-09-01T09:00:00.000+0900", Updated: "2026-09-01T11:00:00.000+0900",
				History: []fixtures.History{{
					At:     "2026-09-01T10:00:00.000+0900",
					Author: "ada",
					Items: []fixtures.HistoryItem{{
						Field: "status", From: "3", To: "10000",
						FromString: "In Progress", ToString: "To Do",
					}},
				}}},
		},
	}
}

// addDuplicateStatus inserts the migrated 10001 "In Progress" beside the
// seeded default 3, plus a transition screen keyed by the doomed id — the
// workflow-shaped reference a merge must not strand.
func addDuplicateStatus(t *testing.T, st *Store) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.putStatusLocked(&model.Status{
		ID: "10001", Name: "In Progress",
		StatusCategory: model.StatusCategory{ID: 4, Key: "indeterminate", Name: "In Progress", ColorName: "yellow"},
	})
	st.putTransitionScreenLocked("3", map[string]fixtures.TransitionScreenField{
		"resolution": {Required: false},
	})
}

// statusIDOf returns the id of the one status named name, failing when the
// name is ambiguous (the pre-merge state) or absent.
func statusIDOf(t *testing.T, st *Store, name string) string {
	t.Helper()
	var ids []string
	for _, s := range st.Statuses() {
		if s.Name == name {
			ids = append(ids, s.ID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("status %q has ids %v, want exactly one", name, ids)
	}
	return ids[0]
}

func countIssues(t *testing.T, st *Store, query string) int {
	t.Helper()
	_, n, err := st.Search(query, 0, -1)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return n
}

// hasScreenRow reports whether a transition_screens row is keyed by id.
// The rendered TransitionScreenFields cannot tell an absent screen from an
// empty one (both serve {}).
func hasScreenRow(st *Store, id string) bool {
	st.mu.RLock()
	defer st.mu.RUnlock()
	_, ok := st.transitionScreenLocked(id)
	return ok
}

func TestMergeStatusRewritesEveryReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issuetap.db")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	// The cutover order: data first, duplicates landing beside the
	// defaults after. Apply would wholesale-replace transition_screens, so
	// the migrated rows (and the screen keyed by the doomed id) go in
	// after it — resolveStatus auto-created 10001 as category new while
	// applying TAP-2, and the migrated row replaces it, which is exactly
	// how the real persists got their duplicates.
	if err := st.Apply(dupCatalogDoc()); err != nil {
		t.Fatal(err)
	}
	addDuplicateStatus(t, st)

	// Before: two ids answer "In Progress" and the doomed id holds rows.
	before := len(st.Statuses())
	if before != 4 {
		t.Fatalf("catalog has %d statuses, want the pre-fix 4 (10000/3/10001/10003)", before)
	}
	if got := countIssues(t, st, "status = 3"); got != 1 {
		t.Fatalf("status = 3 hits %d issues before the merge, want 1", got)
	}

	if err := st.MergeStatus("3", "10001"); err != nil {
		t.Fatal(err)
	}

	if got := len(st.Statuses()); got != before-1 {
		t.Errorf("catalog has %d statuses after the merge, want %d", got, before-1)
	}
	if st.Status("3") != nil {
		t.Errorf("status 3 still in the catalog after merging into 10001")
	}
	if id := statusIDOf(t, st, "In Progress"); id != "10001" {
		t.Errorf(`"In Progress" resolves to %s, want 10001`, id)
	}
	if got := st.Status("10001"); got == nil || got.StatusCategory.Key != "indeterminate" {
		t.Errorf("survivor 10001 missing or wrong category: %+v", got)
	}

	// Issue rows: the doomed id's issue moves; the untouched row keeps
	// its updated stamp (a delta sync must not re-read it for nothing).
	one, two, three := st.Issue("TAP-1"), st.Issue("TAP-2"), st.Issue("TAP-3")
	if one.StatusID != "10001" {
		t.Errorf("TAP-1 status = %s, want 10001", one.StatusID)
	}
	if one.Updated == "2026-09-01T09:00:00.000+0900" {
		t.Errorf("TAP-1 updated not stamped; an updated >= delta sync would keep the dangling id 3")
	}
	if two.Updated != "2026-09-01T09:00:00.000+0900" {
		t.Errorf("TAP-2 (no reference to 3) was rewritten anyway: updated = %s", two.Updated)
	}

	// Changelog: TAP-1's synthesized row and TAP-3's authored row both
	// point at the survivor; the authored display strings stay as the
	// historical record.
	mergedItem := func(iss *model.Issue) *model.HistoryItem {
		for hi := range iss.Histories {
			for ii := range iss.Histories[hi].Items {
				if it := &iss.Histories[hi].Items[ii]; it.FieldID == "status" {
					return it
				}
			}
		}
		return nil
	}
	if it := mergedItem(one); it == nil || it.To != "10001" {
		t.Errorf("TAP-1 synthesized history item = %+v, want to 10001", it)
	}
	if it := mergedItem(three); it == nil || it.From != "10001" {
		t.Errorf("TAP-3 history item = %+v, want from 10001", it)
	} else if it.FromString != "In Progress" {
		t.Errorf("TAP-3 authored fromString rewritten to %q; display strings are the historical record", it.FromString)
	}

	// The workflow reference: the screen keyed by 3 now serves under 10001.
	if hasScreenRow(st, "3") {
		t.Errorf("transition screen still keyed by the deleted status 3")
	}
	if fields := st.TransitionScreenFields("10001"); len(fields) != 1 {
		t.Errorf("transition screen for 10001 = %v, want the moved resolution screen", fields)
	} else if _, ok := fields["resolution"]; !ok {
		t.Errorf("transition screen for 10001 = %v, want resolution on it", fields)
	}

	// Search agrees with the catalog: the doomed id answers nothing.
	if got := countIssues(t, st, "status = 3"); got != 0 {
		t.Errorf("status = 3 still hits %d issues after the merge", got)
	}
	if got := countIssues(t, st, "status = 10001"); got != 2 {
		t.Errorf("status = 10001 hits %d issues after the merge, want 2", got)
	}

	// The merge is durable: a restart must reopen the merged state, not
	// the fixture shape.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got := len(st2.Statuses()); got != 3 {
		t.Errorf("restarted catalog has %d statuses, want the merged 3", got)
	}
	if iss := st2.Issue("TAP-1"); iss == nil || iss.StatusID != "10001" {
		t.Errorf("restarted TAP-1 = %+v, want status 10001", iss)
	}
	if fields := st2.TransitionScreenFields("10001"); len(fields) != 1 {
		t.Errorf("restarted transition screen for 10001 = %v, want the moved screen", fields)
	}
}

func TestMergeStatusRejectsInvalidMerges(t *testing.T) {
	st := New(Options{Seed: 1})
	defer st.Close()
	if err := st.Apply(dupCatalogDoc()); err != nil {
		t.Fatal(err)
	}
	addDuplicateStatus(t, st)
	before := len(st.Statuses())

	// Unknown ids are not-founds naming the status kind.
	if err := st.MergeStatus("999", "10001"); !IsNotFound(err) || NotFoundKind(err) != "status" {
		t.Errorf("merge from unknown id: err = %v, want a status not-found", err)
	}
	if err := st.MergeStatus("3", "999"); !IsNotFound(err) || NotFoundKind(err) != "status" {
		t.Errorf("merge into unknown id: err = %v, want a status not-found", err)
	}
	// A self-merge would delete the status it claims to keep.
	if err := st.MergeStatus("3", "3"); err == nil {
		t.Errorf("merge 3 into 3 succeeded; it deletes the status")
	}
	// Categories are the axis board lanes, sweeps, and the done/resolution
	// lifecycle key on — a cross-category merge is refused.
	if err := st.MergeStatus("3", "10000"); err == nil {
		t.Errorf("merge indeterminate 3 into new 10000 succeeded")
	}
	if err := st.MergeStatus("3", "10003"); err == nil {
		t.Errorf("merge indeterminate 3 into done 10003 succeeded")
	}

	// Nothing above mutated anything: catalog, issue rows, changelog, and
	// screen all still answer to the doomed id (validate-before-mutate,
	// the applyTransitionLocked rule).
	if got := len(st.Statuses()); got != before {
		t.Errorf("catalog = %d statuses after rejected merges, want %d", got, before)
	}
	if iss := st.Issue("TAP-1"); iss == nil || iss.StatusID != "3" {
		t.Errorf("TAP-1 = %+v after rejected merges, want status 3 untouched", iss)
	}
	if fields := st.TransitionScreenFields("3"); len(fields) != 1 {
		t.Errorf("transition screen for 3 = %v after rejected merges, want untouched", fields)
	}
}
