package fixtures

import (
	"strings"
	"testing"
)

// hostileBody is real-world tracker text (measured in gadak GDK-1269): lines
// that start with spaces or tabs around blank lines. yaml.v3's emitter turns
// it into a block scalar its own parser rejects ("did not find expected
// key"), so a Snapshot written with MarshalYAML could not be loaded back.
const hostileBody = "  leading spaces\n\n\tstarts with a tab\ntrailing spaces  \n\nplain end"

// leadingNewlines is the measured minimal trigger (gadak GDK-1269, mirror
// row GDK-973): a value that STARTS with newlines, nested at history-item
// depth, makes yaml.v3 emit `|4-` with content indented shallower than the
// indicator promises — its own parser then fails with "did not find
// expected key". A flat map with the same value round-trips, which is why
// a shallow test never caught it.
const leadingNewlines = "\n\n\n\n(edit note: masked a path — claims unchanged)"

func hostileDoc() Doc {
	return Doc{
		Projects: []Project{{Key: "T", Name: "T"}},
		Issues: []Issue{{
			Key: "T-1", Summary: "hostile", Project: "T",
			Description: hostileBody,
			Comments:    []Comment{{Body: hostileBody}},
			History: []History{{
				At:    "2026-01-01T00:00:00.000Z",
				Items: []HistoryItem{{Field: "description", FromString: hostileBody, ToString: leadingNewlines}},
			}},
		}},
	}
}

// TestMarshalYAMLRoundTripsHostileBodies pins the export/import contract:
// whatever MarshalYAML writes, Parse must read back to the same document.
func TestMarshalYAMLRoundTripsHostileBodies(t *testing.T) {
	d := hostileDoc()
	b, err := MarshalYAML(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(b, ".yaml")
	if err != nil {
		t.Fatalf("a MarshalYAML document must load back: %v", err)
	}
	is := got.Issues[0]
	if is.Description != hostileBody {
		t.Fatalf("description changed:\n got %q\nwant %q", is.Description, hostileBody)
	}
	if is.Comments[0].Body != hostileBody {
		t.Fatalf("comment body changed: %q", is.Comments[0].Body)
	}
	if is.History[0].Items[0].FromString != hostileBody {
		t.Fatalf("history fromString changed: %q", is.History[0].Items[0].FromString)
	}
	if is.History[0].Items[0].ToString != leadingNewlines {
		t.Fatalf("history toString changed: %q", is.History[0].Items[0].ToString)
	}
}

// TestParseKeepsKeepChomping: a keep-chomped (|+) block at the end of a
// document stores its kept newlines as the file's trailing newlines —
// trimming the whole input before parsing rewrites the value.
func TestParseKeepsKeepChomping(t *testing.T) {
	raw := "issues:\n  - key: T-1\n    summary: s\n    project: T\n    description: |+\n      body\n\n\n"
	got, err := Parse([]byte(raw), ".yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Issues[0].Description != "body\n\n\n" {
		t.Fatalf("kept newlines rewritten: %q", got.Issues[0].Description)
	}
}

// TestParseKeepsTrailingNewlines: a keep-chomped block scalar at the end of
// the document encodes its kept newlines as the file's trailing newlines —
// trimming the whole input before parsing silently rewrites the value.
func TestParseKeepsTrailingNewlines(t *testing.T) {
	d := Doc{
		Projects: []Project{{Key: "T", Name: "T"}},
		Issues:   []Issue{{Key: "T-1", Summary: "s", Project: "T", Description: "body\n\n\n"}},
	}
	b, err := MarshalYAML(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := Parse(b, ".yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Issues[0].Description != "body\n\n\n" {
		t.Fatalf("trailing newlines rewritten: %q", got.Issues[0].Description)
	}
	// JSON sniffing must still work with leading whitespace.
	j, err := Parse([]byte("\n  {\"projects\":[{\"key\":\"T\",\"name\":\"T\"}]}"), "")
	if err != nil || len(j.Projects) != 1 {
		t.Fatalf("json sniff with leading whitespace: %v %+v", err, j.Projects)
	}
}

// TestMarshalYAMLPrefersReadableYAML: the JSON fallback is for documents the
// YAML emitter cannot round-trip — an ordinary document must stay YAML.
func TestMarshalYAMLPrefersReadableYAML(t *testing.T) {
	b, err := MarshalYAML(Doc{Projects: []Project{{Key: "T", Name: "T"}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "{") {
		t.Fatalf("plain document must marshal as YAML, got JSON:\n%s", b)
	}
}
