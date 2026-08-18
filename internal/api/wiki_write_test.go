package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func adfValue(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	})
	return string(b)
}

func pageCreateBody(title, space, parent, adf, message string) map[string]any {
	body := map[string]any{
		"type":  "page",
		"title": title,
		"space": map[string]any{"key": space},
		"body": map[string]any{
			"atlas_doc_format": map[string]any{
				"value":          adf,
				"representation": "atlas_doc_format",
			},
		},
	}
	if parent != "" {
		body["ancestors"] = []any{map[string]any{"id": parent}}
	}
	if message != "" {
		body["version"] = map[string]any{"message": message}
	}
	return body
}

func pageUpdateBody(title, adf string, number int, message string) map[string]any {
	return map[string]any{
		"type":  "page",
		"title": title,
		"version": map[string]any{
			"number":  number,
			"message": message,
		},
		"body": map[string]any{
			"atlas_doc_format": map[string]any{
				"value":          adf,
				"representation": "atlas_doc_format",
			},
		},
	}
}

func authPut(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+path, r)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestWikiCreateUpdateVersionHistory is the consumer contract: create, two
// updates, then GET /version returns three rows with the sent messages.
func TestWikiCreateUpdateVersionHistory(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	created := decode(t, authPost(t, ts, "/wiki/rest/api/content", pageCreateBody(
		"Retention notes", "DOCS", "", adfValue("v1 body"), "initial draft",
	)))
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create missing id: %v", created)
	}
	ver, _ := created["version"].(map[string]any)
	if ver == nil || int(ver["number"].(float64)) != 1 {
		t.Fatalf("create version = %v, want 1", created["version"])
	}

	upd1 := authPut(t, ts, "/wiki/rest/api/content/"+id, pageUpdateBody(
		"Retention notes", adfValue("v2 body"), 2, "tightened the retention paragraph",
	))
	if upd1.StatusCode != http.StatusOK {
		t.Fatalf("update1 status %d body=%v", upd1.StatusCode, decode(t, upd1))
	}
	upd1.Body.Close()

	upd2 := authPut(t, ts, "/wiki/rest/api/content/"+id, pageUpdateBody(
		"Retention notes", adfValue("v3 body"), 3, "added the restore steps",
	))
	if upd2.StatusCode != http.StatusOK {
		t.Fatalf("update2 status %d body=%v", upd2.StatusCode, decode(t, upd2))
	}
	gotPage := decode(t, upd2)
	if int(gotPage["version"].(map[string]any)["number"].(float64)) != 3 {
		t.Fatalf("page version after two updates = %v", gotPage["version"])
	}

	hist := decode(t, authGet(t, ts, "/wiki/rest/api/content/"+id+"/version"))
	results, _ := hist["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("version rows = %d, want 3; body=%v", len(results), hist)
	}
	if int(hist["size"].(float64)) != 3 {
		t.Fatalf("size=%v", hist["size"])
	}

	want := []struct {
		n   int
		msg string
	}{
		{3, "added the restore steps"},
		{2, "tightened the retention paragraph"},
		{1, "initial draft"},
	}
	for i, w := range want {
		row := results[i].(map[string]any)
		if int(row["number"].(float64)) != w.n {
			t.Fatalf("results[%d].number=%v want %d (newest-first)", i, row["number"], w.n)
		}
		if row["message"] != w.msg {
			t.Fatalf("results[%d].message=%q want %q", i, row["message"], w.msg)
		}
		if _, ok := row["minorEdit"]; !ok {
			t.Fatalf("results[%d] missing minorEdit", i)
		}
		by, _ := row["by"].(map[string]any)
		if by == nil || by["accountId"] == "" {
			t.Fatalf("results[%d].by=%v", i, row["by"])
		}
		if row["when"] == "" {
			t.Fatalf("results[%d] missing when", i)
		}
	}
}

// TestWikiUpdateStaleVersion is Confluence optimistic concurrency: PUT
// with version.number != current+1 is 409.
func TestWikiUpdateStaleVersion(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	created := decode(t, authPost(t, ts, "/wiki/rest/api/content", pageCreateBody(
		"Concurrency", "DOCS", "", adfValue("one"), "",
	)))
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create missing id: %v", created)
	}

	res := authPut(t, ts, "/wiki/rest/api/content/"+id, pageUpdateBody(
		"Concurrency", adfValue("stale"), 1, "should conflict",
	))
	if res.StatusCode != http.StatusConflict {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("stale PUT status %d, want 409; body=%s", res.StatusCode, raw)
	}
	v := decode(t, res)
	if v["statusCode"] == nil && v["message"] == nil {
		t.Fatalf("409 missing wiki error envelope: %v", v)
	}

	// Omitting version.number must also 409 — a consumer that gets away
	// with that here would break against a real site.
	res = authPut(t, ts, "/wiki/rest/api/content/"+id, map[string]any{
		"type":  "page",
		"title": "Concurrency",
		"body": map[string]any{
			"atlas_doc_format": map[string]any{
				"value":          adfValue("no version"),
				"representation": "atlas_doc_format",
			},
		},
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("PUT without version.number status %d, want 409", res.StatusCode)
	}
	res.Body.Close()
}

func TestWikiVersionMissingPage(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/wiki/rest/api/content/99999/version")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
	v := decode(t, res)
	if v["statusCode"] == nil && v["message"] == nil {
		t.Fatalf("404 envelope: %v", v)
	}
}

func TestWikiCreateValidation(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty title", pageCreateBody("", "DOCS", "", adfValue("x"), "")},
		{"unknown space", pageCreateBody("T", "NOPE", "", adfValue("x"), "")},
		{"unknown parent", pageCreateBody("T", "DOCS", "99999", adfValue("x"), "")},
		{"malformed ADF", pageCreateBody("T", "DOCS", "", "not-json", "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := authPost(t, ts, "/wiki/rest/api/content", tc.body)
			if res.StatusCode != http.StatusBadRequest {
				raw, _ := io.ReadAll(res.Body)
				res.Body.Close()
				t.Fatalf("status %d, want 400; body=%s", res.StatusCode, raw)
			}
			v := decode(t, res)
			if v["statusCode"] == nil && v["message"] == nil {
				t.Fatalf("400 envelope: %v", v)
			}
		})
	}
}

func TestWikiFixturePageHasCurrentVersion(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/20001/version"))
	results, _ := v["results"].([]any)
	if len(results) < 1 {
		t.Fatalf("fixture page has no version rows: %v", v)
	}
	row := results[0].(map[string]any)
	if int(row["number"].(float64)) != 1 {
		t.Fatalf("tiny.yaml page 20001 version number=%v", row["number"])
	}
}

// TestWikiVersionHistorySurvivesPersist: writes land in the snapshot file
// and a restarted store serves the same version messages.
func TestWikiVersionHistorySurvivesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
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
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())

	created := decode(t, authPost(t, ts, "/wiki/rest/api/content", pageCreateBody(
		"Persisted notes", "DOCS", "", adfValue("one"), "first stamp",
	)))
	id, _ := created["id"].(string)
	if id == "" {
		ts.Close()
		st.Close()
		t.Fatalf("create missing id: %v", created)
	}
	res := authPut(t, ts, "/wiki/rest/api/content/"+id, pageUpdateBody(
		"Persisted notes", adfValue("two"), 2, "second stamp",
	))
	if res.StatusCode != http.StatusOK {
		ts.Close()
		st.Close()
		t.Fatalf("update status %d", res.StatusCode)
	}
	res.Body.Close()
	ts.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persist file missing: %v", err)
	}

	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ts2 := httptest.NewServer(api.New(cfg, st2, nil, nil, false).Handler())
	defer ts2.Close()

	hist := decode(t, authGet(t, ts2, "/wiki/rest/api/content/"+id+"/version"))
	results, _ := hist["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("restored version rows = %d, want 2; body=%v", len(results), hist)
	}
	got := map[int]string{}
	for _, raw := range results {
		row := raw.(map[string]any)
		got[int(row["number"].(float64))] = row["message"].(string)
	}
	if got[1] != "first stamp" || got[2] != "second stamp" {
		t.Fatalf("restored messages = %v", got)
	}
}
