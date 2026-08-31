package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

func TestIssueLinkTypesMatchesModelCatalog(t *testing.T) {
	st := loadTiny(t)
	got := st.IssueLinkTypes()
	want := model.DefaultIssueLinkTypes()
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] %+v want %+v", i, got[i], want[i])
		}
	}
}

// Jira labels each projection by the OTHER end's POST role (GDK-1204): the
// outward issue carries an inwardIssue element and vice versa.
func TestAddIssueLinkStoresBothSides(t *testing.T) {
	st := loadTiny(t)
	if err := st.AddIssueLink("10000", "", "TAP-1", "TAP-3"); err != nil {
		t.Fatal(err)
	}
	a := st.Issue("TAP-1")
	b := st.Issue("TAP-3")
	if !issueHasDirectedLink(a, "Blocks", false, "TAP-3") {
		t.Fatalf("TAP-1 missing inward element for TAP-3: %+v", a.Links)
	}
	if !issueHasDirectedLink(b, "Blocks", true, "TAP-1") {
		t.Fatalf("TAP-3 missing outward element for TAP-1: %+v", b.Links)
	}
}

func TestAddIssueLinkByNameAndIssueID(t *testing.T) {
	st := loadTiny(t)
	if err := st.AddIssueLink("", "Blocks", "10002", "10003"); err != nil {
		t.Fatal(err)
	}
	if !issueHasDirectedLink(st.Issue("TAP-2"), "Blocks", false, "TAP-3") {
		t.Fatal("name+id lookup did not store TAP-2→TAP-3")
	}
}

func TestAddIssueLinkUnknownType(t *testing.T) {
	st := loadTiny(t)
	err := st.AddIssueLink("99999", "", "TAP-1", "TAP-3")
	if !IsNotFound(err) || NotFoundKind(err) != "issue link type" {
		t.Fatalf("unknown id err=%v", err)
	}
	if err == nil || err.Error() != "No issue link type with id '99999' found." {
		t.Fatalf("unknown id message %v", err)
	}
	err = st.AddIssueLink("", "NoSuchType", "TAP-1", "TAP-3")
	if !IsNotFound(err) || err.Error() != "No issue link type with name 'NoSuchType' found." {
		t.Fatalf("unknown name err=%v", err)
	}
}

func TestAddIssueLinkMissingIssue(t *testing.T) {
	st := loadTiny(t)
	err := st.AddIssueLink("10000", "", "TAP-1", "NOPE-1")
	if !IsNotFound(err) || NotFoundKind(err) != "issue" {
		t.Fatalf("missing inward err=%v", err)
	}
}

func TestAddIssueLinkSelf(t *testing.T) {
	st := loadTiny(t)
	err := st.AddIssueLink("10000", "", "TAP-1", "10001")
	if !errors.Is(err, ErrSelfLink) {
		t.Fatalf("self link err=%v", err)
	}
}

func TestAddIssueLinkIdempotent(t *testing.T) {
	st := loadTiny(t)
	// tiny.yaml already gives TAP-1 an outward Relates element for TAP-2 —
	// in Jira convention that is the link whose outward end is TAP-2, so the
	// heal POST names TAP-2 as outwardIssue.
	if err := st.AddIssueLink("10003", "", "TAP-2", "TAP-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueLink("", "Relates", "TAP-2", "TAP-1"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range st.Issue("TAP-1").Links {
		if l.TypeName == "Relates" && l.OutwardKey == "TAP-2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("TAP-1 Relates outward TAP-2 count=%d, want 1", n)
	}
	if !issueHasDirectedLink(st.Issue("TAP-2"), "Relates", false, "TAP-1") {
		t.Fatal("TAP-2 was not healed with Relates inward TAP-1")
	}
}

func TestAddIssueLinkSurvivesPersistReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if err := st.AddIssueLink("10000", "", "TAP-1", "TAP-3"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if !issueHasDirectedLink(st2.Issue("TAP-1"), "Blocks", false, "TAP-3") {
		t.Fatalf("TAP-1 link lost: %+v", st2.Issue("TAP-1").Links)
	}
	if !issueHasDirectedLink(st2.Issue("TAP-3"), "Blocks", true, "TAP-1") {
		t.Fatalf("TAP-3 link lost: %+v", st2.Issue("TAP-3").Links)
	}
}
