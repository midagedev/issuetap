package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

// TestTwoOpenStoresAllocateDistinctIDs is gadak GDK-1180: the persist is one
// working copy shared by every process that opened it, so ids must be minted
// in that copy, not from per-process counters seeded once at Open. Before
// the fix, two stores holding one persist both seeded seq=N and handed the
// same comment id (90000+N+1) and the same history id (h N+1) to different
// issues — and the persist kept both.
func TestTwoOpenStoresAllocateDistinctIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	a, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(doc); err != nil {
		t.Fatal(err)
	}

	// A second store on the same persist — a second gadak process.
	b, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ca, err := a.AddComment("TAP-1", "", []byte("from process a"))
	if err != nil {
		t.Fatal(err)
	}
	cb, err := b.AddComment("TAP-2", "", []byte("from process b"))
	if err != nil {
		t.Fatal(err)
	}
	if ca.ID == cb.ID {
		t.Fatalf("two processes minted the same comment id %q for different issues", ca.ID)
	}

	if err := a.SetAssignee("TAP-2", danaAccountID, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.SetAssignee("TAP-3", danaAccountID, ""); err != nil {
		t.Fatal(err)
	}
	lastHist := func(s *Store, key string) string {
		iss := s.Issue(key)
		if iss == nil || len(iss.Histories) == 0 {
			t.Fatalf("%s has no history", key)
		}
		return iss.Histories[len(iss.Histories)-1].ID
	}
	ha, hb := lastHist(a, "TAP-2"), lastHist(b, "TAP-3")
	if ha == hb {
		t.Fatalf("two processes minted the same history id %q for different issues", ha)
	}
}
