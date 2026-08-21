package api_test

// GDK-511: POST /issue/{key}/comment must store Cloud visibility and
// JSM sd.public.comment (as jsdPublic), echo them on 201 and GET, and
// leave both keys absent on an ordinary comment. Invalid visibility.type
// is 400 errors.visibility. Judgment lives in the store.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func commentADF(text string) map[string]any {
	return map[string]any{
		"type": "doc", "version": 1,
		"content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": text},
			}},
		},
	}
}

func postComment(t *testing.T, ts *httptest.Server, key string, body map[string]any) (int, map[string]any) {
	t.Helper()
	return decodeStatus(t, authPost(t, ts, "/rest/api/3/issue/"+key+"/comment", body))
}

func getComments(t *testing.T, ts *httptest.Server, key string) []map[string]any {
	t.Helper()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"/comment"))
	raw, ok := v["comments"].([]any)
	if !ok {
		t.Fatalf("comments missing: %v", v)
	}
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("comments[%d] is %T", i, item)
		}
		out = append(out, m)
	}
	return out
}

func commentHasKey(c map[string]any, key string) bool {
	_, ok := c[key]
	return ok
}

func visibilityOf(t *testing.T, c map[string]any) map[string]any {
	t.Helper()
	vis, ok := c["visibility"].(map[string]any)
	if !ok {
		t.Fatalf("visibility missing or %T: %v", c["visibility"], c)
	}
	return vis
}

func TestCommentVisibilityRoundTrip(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("restricted-note"),
		"visibility": map[string]any{
			"type":  "role",
			"value": "Administrators",
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201 body=%v", status, created)
	}
	vis := visibilityOf(t, created)
	if vis["type"] != "role" || vis["value"] != "Administrators" {
		t.Fatalf("201 visibility=%v", vis)
	}
	if commentHasKey(created, "jsdPublic") {
		t.Fatalf("visibility-only POST invented jsdPublic: %v", created)
	}

	page := getComments(t, ts, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("GET comments len=%d", len(page))
	}
	got := visibilityOf(t, page[0])
	if got["type"] != "role" || got["value"] != "Administrators" {
		t.Fatalf("GET visibility=%v", got)
	}

	issue := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=comment"))
	fields, _ := issue["fields"].(map[string]any)
	embedded, _ := fields["comment"].(map[string]any)
	list, _ := embedded["comments"].([]any)
	if len(list) != 1 {
		t.Fatalf("issue comment embed len=%d", len(list))
	}
	row, _ := list[0].(map[string]any)
	emb := visibilityOf(t, row)
	if emb["type"] != "role" || emb["value"] != "Administrators" {
		t.Fatalf("issue embed visibility=%v", emb)
	}
}

func TestCommentInternalPropertySetsJsdPublicFalse(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("jsm-internal"),
		"properties": []any{
			map[string]any{
				"key":   "sd.public.comment",
				"value": map[string]any{"internal": true},
			},
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201 body=%v", status, created)
	}
	got, ok := created["jsdPublic"]
	if !ok {
		t.Fatalf("201 missing jsdPublic: %v", created)
	}
	if got != false {
		t.Fatalf("201 jsdPublic=%v, want false", got)
	}
	if commentHasKey(created, "visibility") {
		t.Fatalf("internal-only POST invented visibility: %v", created)
	}

	page := getComments(t, ts, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("GET comments len=%d", len(page))
	}
	got, ok = page[0]["jsdPublic"]
	if !ok {
		t.Fatalf("GET missing jsdPublic: %v", page[0])
	}
	if got != false {
		t.Fatalf("GET jsdPublic=%v, want false", got)
	}
}

func TestCommentPlainPostOmitsVisibilityAndJsdPublic(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("ordinary"),
	})
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201 body=%v", status, created)
	}
	if commentHasKey(created, "visibility") {
		t.Fatalf("plain POST invented visibility: %v", created)
	}
	if commentHasKey(created, "jsdPublic") {
		t.Fatalf("plain POST invented jsdPublic: %v", created)
	}
	page := getComments(t, ts, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("GET comments len=%d", len(page))
	}
	if commentHasKey(page[0], "visibility") || commentHasKey(page[0], "jsdPublic") {
		t.Fatalf("GET invented keys: %v", page[0])
	}
}

func TestCommentRejectsBadVisibilityType(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, body := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("bad-type"),
		"visibility": map[string]any{
			"type":  "project",
			"value": "Administrators",
		},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 body=%v", status, body)
	}
	if errorMap(t, body)["visibility"] == nil {
		t.Fatalf("want errors.visibility, got %v", body)
	}
	page := getComments(t, ts, "TAP-2")
	if len(page) != 0 {
		t.Fatalf("rejected comment was stored: %v", page)
	}
}

func TestCommentVisibilitySurvivesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
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
	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("persist-restricted"),
		"visibility": map[string]any{
			"type":  "group",
			"value": "jira-administrators",
		},
	})
	ts.Close()
	if status != http.StatusCreated {
		_ = st.Close()
		t.Fatalf("status %d, want 201 body=%v", status, created)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("persist file missing: %v", err)
	}

	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ts2 := httptest.NewServer(api.New(cfg, st2, nil, nil, false).Handler())
	defer ts2.Close()
	page := getComments(t, ts2, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("reloaded comments len=%d", len(page))
	}
	vis := visibilityOf(t, page[0])
	if vis["type"] != "group" || vis["value"] != "jira-administrators" {
		t.Fatalf("reloaded visibility=%v", vis)
	}
}

func TestCommentVisibilityUnchangedUnderKoreanLocale(t *testing.T) {
	ts := testServerDoc(t, loadExampleDoc(t, "korean.yaml"), locale.KO)
	defer ts.Close()
	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body": commentADF("ko-restricted"),
		"visibility": map[string]any{
			"type":  "role",
			"value": "Administrators",
		},
	})
	if status != http.StatusCreated {
		t.Fatalf("status %d, want 201 body=%v", status, created)
	}
	vis := visibilityOf(t, created)
	if vis["type"] != "role" || vis["value"] != "Administrators" {
		t.Fatalf("locale must not rewrite visibility: %v", vis)
	}
	page := getComments(t, ts, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("GET comments len=%d", len(page))
	}
	got := visibilityOf(t, page[0])
	if got["type"] != "role" || got["value"] != "Administrators" {
		t.Fatalf("GET visibility under ko=%v", got)
	}
	raw, err := json.Marshal(page[0])
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid JSON: %s", raw)
	}
}
