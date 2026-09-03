package store

import (
	"encoding/json"
	"testing"

	"github.com/midagedev/issuetap/internal/adf"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

// richADF is a body the plain slot cannot hold: a heading, two paragraphs,
// a bullet list. Measured motive (gadak GDK-1382): a migration exported 844
// issues through the text slot and every one loaded back as a single
// paragraph, the heading and list markers flattened into it.
const richADF = `{"type":"doc","version":1,"content":[` +
	`{"type":"heading","attrs":{"level":2},"content":[{"type":"text","text":"Symptom"}]},` +
	`{"type":"paragraph","content":[{"type":"text","text":"first"}]},` +
	`{"type":"paragraph","content":[{"type":"text","text":"second"}]},` +
	`{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"item"}]}]}]}]}`

func richDoc() fixtures.Doc {
	return fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Issues: []fixtures.Issue{{
			Key: "TAP-1", Summary: "rich", Project: "TAP",
			DescriptionADF: richADF,
			Comments:       []fixtures.Comment{{Body: "plain comment"}, {Body: "rich comment", BodyADF: richADF}},
		}},
		Spaces: []fixtures.Space{{Key: "DOC", Name: "Doc"}},
		Pages: []fixtures.Page{{
			ID: "700", Title: "Page", Space: "DOC", BodyADF: richADF,
			Comments: []fixtures.PageComment{{Body: "note", BodyADF: richADF}},
		}},
	}
}

func sameADF(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: stored ADF does not parse: %v", what, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("%s: ADF changed\n got %s\nwant %s", what, gb, wb)
	}
}

// TestFixtureADFSlotIsStoredVerbatim: a fixture body with formatting arrives
// through the adf slot and is served exactly as written; the text beside it
// is derived so search still sees words. The plain-only comment keeps the
// adf.Doc shape it always had.
func TestFixtureADFSlotIsStoredVerbatim(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(richDoc()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	iss := st.Issue("TAP-1")
	if iss == nil {
		t.Fatal("TAP-1 missing")
	}
	sameADF(t, "description", iss.DescriptionADF, richADF)
	if iss.DescriptionText != adf.Plain(json.RawMessage(richADF)) {
		t.Fatalf("description text not derived from the ADF: %q", iss.DescriptionText)
	}
	if len(iss.Comments) != 2 {
		t.Fatalf("comments: %d", len(iss.Comments))
	}
	if string(iss.Comments[0].Body) != string(adf.Doc("plain comment")) {
		t.Fatalf("plain comment must keep the adf.Doc shape: %s", iss.Comments[0].Body)
	}
	sameADF(t, "comment", iss.Comments[1].Body, richADF)
	if iss.Comments[1].BodyText != "rich comment" {
		t.Fatalf("a text given beside the ADF is kept: %q", iss.Comments[1].BodyText)
	}
	pg := st.Page("700")
	if pg == nil {
		t.Fatal("page 700 missing")
	}
	sameADF(t, "page", pg.BodyADF, richADF)
	cms := st.pageCommentsLocked("700")
	if len(cms) != 1 {
		t.Fatalf("page comments: %d", len(cms))
	}
	sameADF(t, "page comment", cms[0].BodyADF, richADF)
}

// TestSnapshotCarriesFormattingBackThroughTheADFSlot pins the export half:
// what Snapshot writes, Apply reads back to the same documents — and a plain
// body is written plain, without an adf slot, so snapshots of unformatted
// trackers do not double every body.
func TestSnapshotCarriesFormattingBackThroughTheADFSlot(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(richDoc()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	snap := st.Snapshot()
	var is fixtures.Issue
	for _, i := range snap.Issues {
		if i.Key == "TAP-1" {
			is = i
		}
	}
	if is.DescriptionADF == "" {
		t.Fatal("snapshot dropped the description's formatting")
	}
	if is.Comments[0].BodyADF != "" {
		t.Fatalf("a plain comment must not grow an adf slot: %s", is.Comments[0].BodyADF)
	}
	if is.Comments[1].BodyADF == "" {
		t.Fatal("snapshot dropped the comment's formatting")
	}
	b, err := fixtures.MarshalYAML(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := fixtures.Parse(b, ".yaml")
	if err != nil {
		t.Fatalf("a Snapshot document must load back: %v", err)
	}
	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(back); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	sameADF(t, "description after round trip", st2.Issue("TAP-1").DescriptionADF, richADF)
	sameADF(t, "comment after round trip", st2.Issue("TAP-1").Comments[1].Body, richADF)
	sameADF(t, "page after round trip", st2.Page("700").BodyADF, richADF)
	sameADF(t, "page comment after round trip", st2.pageCommentsLocked("700")[0].BodyADF, richADF)
}
