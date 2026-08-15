package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func loadTiny(t *testing.T) *Store {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSnapshotRoundTrip(t *testing.T) {
	st := loadTiny(t)
	doc := st.Snapshot()
	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if st2.Counts()["issues"] != st.Counts()["issues"] {
		t.Fatalf("issues %d vs %d", st2.Counts()["issues"], st.Counts()["issues"])
	}
}

func TestSynthesizeHistory(t *testing.T) {
	st := loadTiny(t)
	iss := st.Issue("TAP-1")
	if iss == nil || len(iss.Histories) < 2 {
		t.Fatalf("TAP-1 histories=%v", iss)
	}
}

func TestCreateIssueDoesNotReuseFixtureIDs(t *testing.T) {
	st := loadTiny(t)
	created, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"},
		"summary": "after tiny",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TAP-1", "TAP-2", "TAP-3"} {
		exist := st.Issue(key)
		if exist == nil {
			t.Fatalf("missing %s", key)
		}
		if exist.ID == created.ID {
			t.Fatalf("created id %s collides with %s", created.ID, key)
		}
	}
}

func TestDuplicateKeyRejected(t *testing.T) {
	_, err := fixtures.Parse([]byte(`
issues:
  - {key: A-1, summary: x}
  - {key: A-1, summary: y}
`), ".yaml")
	if err == nil {
		t.Fatal("expected duplicate key error")
	}
}
