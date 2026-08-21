package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/model"
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

func TestPageVersionHistorySnapshotRoundTrip(t *testing.T) {
	doc := fixtures.Doc{
		Spaces: []fixtures.Space{{Key: "DOCS", Name: "Docs"}},
		Pages: []fixtures.Page{{
			ID: "20099", Title: "Retention", Space: "DOCS", Version: 2,
			When: "2026-08-02T00:00:00.000Z", Author: "ada", Body: "current",
			Versions: []fixtures.PageVersion{
				{Number: 1, Message: "initial draft", When: "2026-08-01T00:00:00.000Z", Author: "ada"},
				{Number: 2, Message: "tightened the retention paragraph", When: "2026-08-02T00:00:00.000Z", Author: "ada"},
			},
		}},
	}
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	got, err := st.PageVersions("20099")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Number != 2 || got[0].Message != "tightened the retention paragraph" {
		t.Fatalf("after apply: %+v", got)
	}

	snap := st.Snapshot()
	var found fixtures.Page
	for _, p := range snap.Pages {
		if p.ID == "20099" {
			found = p
		}
	}
	if len(found.Versions) != 2 {
		t.Fatalf("snapshot versions = %+v", found.Versions)
	}
	msgs := map[int]string{}
	for _, v := range found.Versions {
		msgs[v.Number] = v.Message
	}
	if msgs[1] != "initial draft" || msgs[2] != "tightened the retention paragraph" {
		t.Fatalf("snapshot messages = %v", msgs)
	}

	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(snap); err != nil {
		t.Fatal(err)
	}
	got2, err := st2.PageVersions("20099")
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 2 || got2[0].Message != "tightened the retention paragraph" || got2[1].Message != "initial draft" {
		t.Fatalf("after reload: %+v", got2)
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

func TestLinkDevPRBumpsIssueUpdated(t *testing.T) {
	st := loadTiny(t)
	before := st.Issue("TAP-1")
	if before == nil {
		t.Fatal("TAP-1 missing")
	}
	prev := before.Updated
	if _, err := st.LinkDevPR("TAP-1", model.DevPR{URL: "https://github.com/x/y/pull/1", Status: "OPEN"}); err != nil {
		t.Fatal(err)
	}
	after := st.Issue("TAP-1")
	if after.Updated == prev {
		t.Fatalf("issue.Updated did not advance after LinkDevPR: still %q (GDK-537)", prev)
	}
	if len(after.DevPRs) != 1 {
		t.Fatalf("dev PR not stored: %v", after.DevPRs)
	}
}
