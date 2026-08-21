package api_test

// GDK-509: GET ?expand=transitions.fields must include a fields object on
// every transition (empty screen = {}), and POST must store
// fields.resolution instead of silently replacing it with 10000.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func testServerDoc(t *testing.T, doc fixtures.Doc, loc locale.Code) *httptest.Server {
	t.Helper()
	st := store.New(store.Options{Seed: 1, Locale: loc})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if loc != "" {
		st.SetLocale(loc)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	s := api.New(cfg, st, nil, nil, false)
	return httptest.NewServer(s.Handler())
}

func loadExampleDoc(t *testing.T, name string) fixtures.Doc {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example(name))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func withDoneResolutionScreen(doc fixtures.Doc) fixtures.Doc {
	doc.TransitionScreens = []fixtures.TransitionScreen{{
		Status: "10003",
		Fields: map[string]fixtures.TransitionScreenField{
			"resolution": {Required: true},
		},
	}}
	return doc
}

func listTransitions(t *testing.T, ts *httptest.Server, key, expand string) []map[string]any {
	t.Helper()
	path := "/rest/api/3/issue/" + key + "/transitions"
	if expand != "" {
		path += "?expand=" + expand
	}
	v := decode(t, authGet(t, ts, path))
	raw, ok := v["transitions"].([]any)
	if !ok {
		t.Fatalf("transitions missing: %v", v)
	}
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("transitions[%d] is %T", i, item)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		t.Fatal("no transitions")
	}
	return out
}

func transitionIDByCategory(t *testing.T, ts *httptest.Server, key, category string) string {
	t.Helper()
	for _, tr := range listTransitions(t, ts, key, "transitions.fields") {
		to, _ := tr["to"].(map[string]any)
		cat, _ := to["statusCategory"].(map[string]any)
		if cat["key"] == category {
			id, _ := tr["id"].(string)
			if id == "" {
				t.Fatalf("transition to %s missing id: %v", category, tr)
			}
			return id
		}
	}
	t.Fatalf("no transition with statusCategory.key=%s", category)
	return ""
}

func postTransition(t *testing.T, ts *httptest.Server, key string, body map[string]any) (int, map[string]any) {
	t.Helper()
	res := authPost(t, ts, "/rest/api/3/issue/"+key+"/transitions", body)
	return decodeStatus(t, res)
}

func issueResolutionID(t *testing.T, ts *httptest.Server, key string) any {
	t.Helper()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"?fields=resolution,status"))
	fields, _ := v["fields"].(map[string]any)
	if fields == nil {
		t.Fatalf("issue missing fields: %v", v)
	}
	if fields["resolution"] == nil {
		return nil
	}
	res, ok := fields["resolution"].(map[string]any)
	if !ok {
		t.Fatalf("resolution is %T", fields["resolution"])
	}
	return res["id"]
}

func TestTransitionsExpandFieldsAlwaysPresent(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	for _, tr := range listTransitions(t, ts, "TAP-1", "transitions.fields") {
		raw, ok := tr["fields"]
		if !ok {
			t.Fatalf("expand omitted fields on transition %v", tr["id"])
		}
		fields, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("fields is %T, want object (empty screen = {})", raw)
		}
		if len(fields) != 0 {
			t.Fatalf("tiny.yaml has no screens, fields=%v want {}", fields)
		}
	}
}

func TestTransitionsWithoutExpandOmitFields(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	for _, tr := range listTransitions(t, ts, "TAP-1", "") {
		if _, ok := tr["fields"]; ok {
			t.Fatalf("fields present without expand on %v", tr["id"])
		}
	}
}

func TestTransitionExpandScreenFieldsShape(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "tiny.yaml")), locale.EN)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	var found map[string]any
	for _, tr := range listTransitions(t, ts, "TAP-1", "transitions.fields") {
		if tr["id"] == id {
			found = tr
			break
		}
	}
	fields, _ := found["fields"].(map[string]any)
	res, ok := fields["resolution"].(map[string]any)
	if !ok {
		t.Fatalf("done transition fields=%v, want resolution", fields)
	}
	if res["required"] != true {
		t.Fatalf("resolution.required=%v, want true", res["required"])
	}
	if res["name"] != "Resolution" {
		t.Fatalf("resolution.name=%v", res["name"])
	}
	schema, _ := res["schema"].(map[string]any)
	if schema["type"] != "resolution" {
		t.Fatalf("schema.type=%v, want resolution", schema["type"])
	}
	av, _ := res["allowedValues"].([]any)
	if len(av) == 0 {
		t.Fatalf("allowedValues empty: %v", res)
	}
	seen := false
	for _, item := range av {
		row, _ := item.(map[string]any)
		if row["id"] == "10002" && row["name"] != "" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("allowedValues missing id 10002: %v", av)
	}
}

func TestTransitionRejectsFieldsWithoutScreen(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
		"fields":     map[string]any{"resolution": map[string]any{"id": "10002"}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 body=%v", status, body)
	}
	errs := errorMap(t, body)
	if errs["resolution"] == nil {
		t.Fatalf("want errors.resolution, got %v", body)
	}
	if issueResolutionID(t, ts, "TAP-1") != nil {
		t.Fatal("undeclared-screen POST must not store a resolution")
	}
}

func TestTransitionRequiredResolution(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "tiny.yaml")), locale.EN)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 body=%v", status, body)
	}
	errs := errorMap(t, body)
	if errs["resolution"] == nil {
		t.Fatalf("want errors.resolution, got %v", body)
	}
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1?fields=status,resolution"))
	fields, _ := v["fields"].(map[string]any)
	st, _ := fields["status"].(map[string]any)
	if st["id"] != "3" {
		t.Fatalf("status moved on required miss: %v", st)
	}
}

func TestTransitionStoresRequestedResolution(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "tiny.yaml")), locale.EN)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
		"fields":     map[string]any{"resolution": map[string]any{"id": "10002"}},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status %d, want 204 body=%v", status, body)
	}
	if got := issueResolutionID(t, ts, "TAP-1"); got != "10002" {
		t.Fatalf("resolution id=%v, want 10002 (not the hardcoded 10000)", got)
	}
}

func TestTransitionRejectsUnknownResolution(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "tiny.yaml")), locale.EN)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
		"fields":     map[string]any{"resolution": map[string]any{"id": "99999"}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 body=%v", status, body)
	}
	if errorMap(t, body)["resolution"] == nil {
		t.Fatalf("want errors.resolution, got %v", body)
	}
}

func TestTransitionDoneDefaultsResolution(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status %d, want 204 body=%v", status, body)
	}
	if got := issueResolutionID(t, ts, "TAP-1"); got != "10000" {
		t.Fatalf("default done resolution=%v, want 10000", got)
	}
}

func TestTransitionClearsResolutionLeavingDone(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	if got := issueResolutionID(t, ts, "TAP-3"); got != "10000" {
		t.Fatalf("precondition TAP-3 resolution=%v", got)
	}
	id := transitionIDByCategory(t, ts, "TAP-3", "new")
	status, body := postTransition(t, ts, "TAP-3", map[string]any{
		"transition": map[string]any{"id": id},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status %d, want 204 body=%v", status, body)
	}
	if got := issueResolutionID(t, ts, "TAP-3"); got != nil {
		t.Fatalf("leaving done left resolution=%v, want nil", got)
	}
}

func TestTransitionKoreanKeysResolutionByID(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "korean.yaml")), locale.KO)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	// Name overlay is 복제 / Duplicate — the write keys on id 10002.
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
		"fields":     map[string]any{"resolution": map[string]any{"id": "10002"}},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status %d, want 204 body=%v", status, body)
	}
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1?fields=resolution,status"))
	fields, _ := v["fields"].(map[string]any)
	res, _ := fields["resolution"].(map[string]any)
	if res["id"] != "10002" {
		t.Fatalf("resolution=%v, want id 10002 (not a localized name)", res)
	}
	st, _ := fields["status"].(map[string]any)
	cat, _ := st["statusCategory"].(map[string]any)
	if cat["key"] != "done" {
		t.Fatalf("statusCategory.key=%v (must not key on 완료)", cat["key"])
	}
}

func TestTransitionStoresUpdateComment(t *testing.T) {
	ts := testServerDoc(t, withDoneResolutionScreen(loadExampleDoc(t, "tiny.yaml")), locale.EN)
	defer ts.Close()
	id := transitionIDByCategory(t, ts, "TAP-1", "done")
	adf := map[string]any{
		"type": "doc", "version": 1,
		"content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": "dup of TAP-2"},
			}},
		},
	}
	status, body := postTransition(t, ts, "TAP-1", map[string]any{
		"transition": map[string]any{"id": id},
		"fields":     map[string]any{"resolution": map[string]any{"id": "10002"}},
		"update": map[string]any{
			"comment": []any{
				map[string]any{"add": map[string]any{"body": adf}},
			},
		},
	})
	if status != http.StatusNoContent {
		t.Fatalf("status %d, want 204 body=%v", status, body)
	}
	page := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1/comment"))
	raw, err := json.Marshal(page["comments"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "dup of TAP-2") {
		t.Fatalf("transition comment not stored: %s", raw)
	}
}
