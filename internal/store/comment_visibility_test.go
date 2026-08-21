package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

func TestWriteCommentStoresVisibility(t *testing.T) {
	st := loadTiny(t)
	cm, err := st.WriteComment("TAP-2", "", CommentWrite{
		Body:       []byte("restricted"),
		Visibility: &model.Visibility{Type: "role", Value: "Administrators"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Visibility == nil || cm.Visibility.Type != "role" || cm.Visibility.Value != "Administrators" {
		t.Fatalf("visibility=%v", cm.Visibility)
	}
	if cm.JsdPublic != nil {
		t.Fatalf("invented jsdPublic=%v", *cm.JsdPublic)
	}
	stored := st.Issue("TAP-2").Comments
	if len(stored) != 1 {
		t.Fatalf("stored comments=%d", len(stored))
	}
	if stored[0].Visibility == nil || stored[0].Visibility.Type != "role" {
		t.Fatalf("stored visibility=%v", stored[0].Visibility)
	}
}

func TestWriteCommentParsesJsdPublic(t *testing.T) {
	st := loadTiny(t)
	cm, err := st.WriteComment("TAP-2", "", CommentWrite{
		Body: []byte("internal"),
		Properties: []CommentProperty{{
			Key:   sdPublicCommentProperty,
			Value: json.RawMessage(`{"internal":true}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cm.JsdPublic == nil || *cm.JsdPublic {
		t.Fatalf("jsdPublic=%v, want false", cm.JsdPublic)
	}
	if cm.Visibility != nil {
		t.Fatalf("invented visibility=%v", cm.Visibility)
	}
}

func TestWriteCommentRejectsBadVisibilityType(t *testing.T) {
	st := loadTiny(t)
	_, err := st.WriteComment("TAP-2", "", CommentWrite{
		Body:       []byte("bad"),
		Visibility: &model.Visibility{Type: "project", Value: "Administrators"},
	})
	fe, ok := AsFieldError(err)
	if !ok || fe.Map()["visibility"] == "" {
		t.Fatalf("want FieldError.visibility, got %T %v", err, err)
	}
	if n := len(st.Issue("TAP-2").Comments); n != 0 {
		t.Fatalf("rejected comment stored, n=%d", n)
	}
}

func TestWriteCommentRejectsEmptyVisibilityValue(t *testing.T) {
	st := loadTiny(t)
	_, err := st.WriteComment("TAP-2", "", CommentWrite{
		Body:       []byte("empty-value"),
		Visibility: &model.Visibility{Type: "role", Value: "  "},
	})
	fe, ok := AsFieldError(err)
	if !ok || fe.Map()["visibility"] == "" {
		t.Fatalf("want FieldError.visibility, got %T %v", err, err)
	}
}

func TestFixtureCommentVisibilityAndInternalRoundTrip(t *testing.T) {
	internal := true
	doc := fixtures.Doc{
		Issues: []fixtures.Issue{{
			Key: "TAP-1", Summary: "seeded",
			Comments: []fixtures.Comment{{
				Body:       "seeded-restricted",
				Visibility: &fixtures.CommentVisibility{Type: "role", Value: "Administrators"},
				Internal:   &internal,
			}},
		}},
	}
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if iss == nil || len(iss.Comments) != 1 {
		t.Fatalf("issue=%v", iss)
	}
	c := iss.Comments[0]
	if c.Visibility == nil || c.Visibility.Type != "role" || c.Visibility.Value != "Administrators" {
		t.Fatalf("loaded visibility=%v", c.Visibility)
	}
	if c.JsdPublic == nil || *c.JsdPublic {
		t.Fatalf("loaded jsdPublic=%v, want false", c.JsdPublic)
	}

	snap := st.Snapshot()
	if len(snap.Issues) != 1 || len(snap.Issues[0].Comments) != 1 {
		t.Fatalf("snapshot comments missing: %+v", snap.Issues)
	}
	fc := snap.Issues[0].Comments[0]
	if fc.Visibility == nil || fc.Visibility.Type != "role" || fc.Visibility.Value != "Administrators" {
		t.Fatalf("snapshot visibility=%v", fc.Visibility)
	}
	if fc.Internal == nil || !*fc.Internal {
		t.Fatalf("snapshot internal=%v, want true", fc.Internal)
	}

	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(snap); err != nil {
		t.Fatal(err)
	}
	c2 := st2.Issue("TAP-1").Comments[0]
	if c2.Visibility == nil || c2.Visibility.Value != "Administrators" {
		t.Fatalf("reloaded visibility=%v", c2.Visibility)
	}
	if c2.JsdPublic == nil || *c2.JsdPublic {
		t.Fatalf("reloaded jsdPublic=%v", c2.JsdPublic)
	}
}

func TestWriteCommentVisibilitySurvivesPersistFile(t *testing.T) {
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
	if _, err := st.WriteComment("TAP-2", "", CommentWrite{
		Body:       []byte("persist-restricted"),
		Visibility: &model.Visibility{Type: "group", Value: "jira-administrators"},
		Properties: []CommentProperty{{
			Key:   sdPublicCommentProperty,
			Value: json.RawMessage(`{"internal":true}`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persist file: %v", err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	iss := st2.Issue("TAP-2")
	if iss == nil || len(iss.Comments) != 1 {
		t.Fatalf("reloaded TAP-2 comments=%v", iss)
	}
	c := iss.Comments[0]
	if c.Visibility == nil || c.Visibility.Type != "group" || c.Visibility.Value != "jira-administrators" {
		t.Fatalf("reloaded visibility=%v", c.Visibility)
	}
	if c.JsdPublic == nil || *c.JsdPublic {
		t.Fatalf("reloaded jsdPublic=%v, want false", c.JsdPublic)
	}
}

func TestTinySnapshotOmitsCommentVisibilityKeys(t *testing.T) {
	st := loadTiny(t)
	doc := st.Snapshot()
	for _, iss := range doc.Issues {
		for _, c := range iss.Comments {
			if c.Visibility != nil {
				t.Fatalf("%s comment %s invented visibility", iss.Key, c.ID)
			}
			if c.Internal != nil {
				t.Fatalf("%s comment %s invented internal", iss.Key, c.ID)
			}
		}
	}
}
