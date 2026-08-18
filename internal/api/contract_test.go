package api_test

// Compatibility contract ↔ assertion mapping
// (docs/COMPATIBILITY.md). Each clause has a happy path and a
// violation/boundary. FAIL-first: the violation assertion is the one that
// would fail if the clause were dropped.
//
// | Clause | Happy | Violation / boundary |
// | --- | --- | --- |
// | GET /myself 200 + accountId | TestMyself | TestAuthRequired |
// | GET /status localized names, stable category.key | TestStatusLocale | TestStatusCategoryKeyStable |
// | POST /search/jql nextPageToken/isLast | TestSearchJQLPages | TestSearchJQLUnparseable |
// | POST /search/approximate-count matches search | TestApproxCount | TestApproxCountBadJQL |
// | GET issue + expand=changelog | TestIssueChangelog | TestIssueMissing |
// | GET changelog values/total/isLast | TestChangelogPage | TestChangelogTotalMatches |
// | GET comments startAt/total | TestCommentsPage | TestCommentsMissingIssue |
// | GET /project/search values/isLast | TestProjectSearch | TestProjectMissing |
// | GET /priority order | TestPriorityOrder | TestPriorityLocaleOverlay |
// | GET /field catalog | TestFieldCatalog | TestFieldNamesLocalize |
// | GET /filter/my | TestFilters | TestFiltersEmpty |
// | Confluence /wiki/rest/api/space | TestSpaces | TestSpaceMissing |
// | Confluence content/search CQL | TestCQLPages | TestCQLUnsupported |
// | Confluence content/{id} ADF | TestPageADF | TestPageMissing |
// | Confluence content/{id}/version | TestWikiCreateUpdateVersionHistory | TestWikiVersionMissingPage |
// | Confluence POST/PUT /content | TestWikiCreateUpdateVersionHistory | TestWikiUpdateStaleVersion / TestWikiCreateValidation |
// | Confluence child/comment | TestPageComments | TestPageCommentsEmpty |
// | unsupported_endpoint 501 | TestUnsupported | TestUnsupportedNot404 |
// | writes: comment / transition / assignee | TestWrites | TestWriteMissingIssue |
// | DC startAt search | TestDCSearch | TestDCWikiMarkupBody |
// | determinism: same seed → same snapshot | TestDeterminism | (diff empty) |

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func testServer(t *testing.T, loc locale.Code, kind dialect.Kind) *httptest.Server {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(store.Options{Seed: 1, Locale: loc})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if loc != "" {
		st.SetLocale(loc)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = kind
	s := api.New(cfg, st, nil, nil, false)
	return httptest.NewServer(s.Handler())
}

func authGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func authPost(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, r)
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

func decode(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func decodeArr(t *testing.T, res *http.Response) []any {
	t.Helper()
	defer res.Body.Close()
	var v []any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMyself(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/myself")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	v := decode(t, res)
	if v["accountId"] == "" {
		t.Fatal("missing accountId")
	}
	if v["displayName"] == "" {
		t.Fatal("missing displayName")
	}
}

func TestAuthRequired(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/rest/api/3/myself")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("status %d, want 401", res.StatusCode)
	}
}

func TestStatusLocale(t *testing.T) {
	ts := testServer(t, locale.KO, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/status")
	arr := decodeArr(t, res)
	if len(arr) == 0 {
		t.Fatal("empty")
	}
	found := false
	for _, raw := range arr {
		m := raw.(map[string]any)
		if m["id"] == "3" {
			found = true
			if m["name"] != "진행 중" {
				t.Fatalf("status 3 name = %v, want 진행 중", m["name"])
			}
			cat := m["statusCategory"].(map[string]any)
			if cat["key"] != "indeterminate" {
				t.Fatalf("category.key = %v", cat["key"])
			}
		}
	}
	if !found {
		t.Fatal("status 3 missing")
	}
}

func TestStatusCategoryKeyStable(t *testing.T) {
	en := testServer(t, locale.EN, dialect.Cloud)
	defer en.Close()
	ko := testServer(t, locale.KO, dialect.Cloud)
	defer ko.Close()
	enA := decodeArr(t, authGet(t, en, "/rest/api/3/status"))
	koA := decodeArr(t, authGet(t, ko, "/rest/api/3/status"))
	keys := func(arr []any) map[string]string {
		out := map[string]string{}
		for _, raw := range arr {
			m := raw.(map[string]any)
			cat := m["statusCategory"].(map[string]any)
			out[m["id"].(string)] = cat["key"].(string)
		}
		return out
	}
	ek, kk := keys(enA), keys(koA)
	for id, k := range ek {
		if kk[id] != k {
			t.Fatalf("status %s category.key en=%s ko=%s", id, k, kk[id])
		}
	}
}

func TestSearchJQLPages(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": "ORDER BY updated DESC", "maxResults": 2, "fields": []string{"summary", "status"},
		"expand": "changelog",
	})
	v := decode(t, res)
	issues, _ := v["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("page1 len=%d", len(issues))
	}
	if v["isLast"] == true {
		t.Fatal("expected another page")
	}
	tok, _ := v["nextPageToken"].(string)
	if tok == "" {
		t.Fatal("missing nextPageToken")
	}
	res2 := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": "ORDER BY updated DESC", "maxResults": 2, "nextPageToken": tok,
	})
	v2 := decode(t, res2)
	if v2["isLast"] != true {
		t.Fatalf("page2 isLast=%v", v2["isLast"])
	}
}

func TestSearchJQLUnparseable(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": "%%% not jql",
	})
	if res.StatusCode != 400 {
		t.Fatalf("status %d, want 400 (must not return every issue)", res.StatusCode)
	}
	res.Body.Close()
}

func TestApproxCount(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/approximate-count", map[string]any{"jql": ""})
	v := decode(t, res)
	if int(v["count"].(float64)) != 3 {
		t.Fatalf("count=%v want 3", v["count"])
	}
}

func TestApproxCountBadJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/approximate-count", map[string]any{"jql": "%%%"})
	if res.StatusCode != 400 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestIssueChangelog(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/TAP-1?expand=changelog")
	v := decode(t, res)
	if v["key"] != "TAP-1" {
		t.Fatalf("key=%v", v["key"])
	}
	cl := v["changelog"].(map[string]any)
	if int(cl["total"].(float64)) < 1 {
		t.Fatal("expected changelog rows")
	}
}

func TestIssueMissing(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/NOPE-1")
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestChangelogPage(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/TAP-1/changelog?startAt=0&maxResults=1")
	v := decode(t, res)
	if v["isLast"] == true && int(v["total"].(float64)) > 1 {
		t.Fatal("isLast true while more remain")
	}
	vals, _ := v["values"].([]any)
	if len(vals) != 1 {
		t.Fatalf("values=%d", len(vals))
	}
}

func TestChangelogTotalMatches(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/TAP-1/changelog?maxResults=100")
	v := decode(t, res)
	vals := v["values"].([]any)
	if int(v["total"].(float64)) != len(vals) {
		t.Fatalf("total=%v len(values)=%d", v["total"], len(vals))
	}
}

func TestCommentsPage(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/TAP-1/comment?startAt=0&maxResults=1")
	v := decode(t, res)
	if int(v["total"].(float64)) != 2 {
		t.Fatalf("total=%v", v["total"])
	}
	cms := v["comments"].([]any)
	if len(cms) != 1 {
		t.Fatalf("page=%d", len(cms))
	}
}

func TestCommentsMissingIssue(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/NOPE-1/comment")
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestProjectSearch(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/project/search?startAt=0&maxResults=50")
	v := decode(t, res)
	if v["isLast"] != true {
		t.Fatal("expected isLast")
	}
	if int(v["total"].(float64)) < 1 {
		t.Fatal("no projects")
	}
}

func TestProjectMissing(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/project/ZZZ")
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestPriorityOrder(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	arr := decodeArr(t, authGet(t, ts, "/rest/api/3/priority"))
	if arr[0].(map[string]any)["name"] != "Highest" {
		t.Fatalf("first=%v", arr[0])
	}
}

func TestPriorityLocaleOverlay(t *testing.T) {
	ts := testServer(t, locale.KO, dialect.Cloud)
	defer ts.Close()
	arr := decodeArr(t, authGet(t, ts, "/rest/api/3/priority"))
	if arr[1].(map[string]any)["name"] != "높음" {
		t.Fatalf("High localized = %v", arr[1])
	}
}

func TestFieldCatalog(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	arr := decodeArr(t, authGet(t, ts, "/rest/api/3/field"))
	if len(arr) < 5 {
		t.Fatal("too few fields")
	}
}

func TestFieldNamesLocalize(t *testing.T) {
	ts := testServer(t, locale.KO, dialect.Cloud)
	defer ts.Close()
	arr := decodeArr(t, authGet(t, ts, "/rest/api/3/field"))
	found := false
	for _, raw := range arr {
		m := raw.(map[string]any)
		if m["id"] == "issuetype" {
			found = true
			if m["name"] != "이슈 유형" {
				t.Fatalf("issuetype name=%v", m["name"])
			}
		}
	}
	if !found {
		t.Fatal("issuetype missing")
	}
}

func TestFilters(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/filter/my")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	_ = decodeArr(t, res)
}

func TestFiltersEmpty(t *testing.T) {
	// tiny fixture has no filters — must be [] not 404.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	arr := decodeArr(t, authGet(t, ts, "/rest/api/3/filter/my"))
	if arr == nil {
		t.Fatal("null filters")
	}
}

func TestSpaces(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/space?limit=100"))
	res := v["results"].([]any)
	if len(res) < 1 {
		t.Fatal("no spaces")
	}
}

func TestSpaceMissing(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/wiki/rest/api/space/NOPE")
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestCQLPages(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/search?cql=space=%22DOCS%22%20AND%20type=page&limit=50"))
	res := v["results"].([]any)
	if len(res) < 1 {
		t.Fatal("no pages")
	}
}

func TestCQLUnsupported(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/wiki/rest/api/content/search?cql=ancestor=1")
	if res.StatusCode != 400 {
		t.Fatalf("status %d, want 400 not a silent all-pages", res.StatusCode)
	}
	res.Body.Close()
}

func TestPageADF(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/20001?expand=body.atlas_doc_format,version,space,ancestors,metadata.labels"))
	body := v["body"].(map[string]any)
	adf := body["atlas_doc_format"].(map[string]any)
	if adf["value"] == "" {
		t.Fatal("empty ADF")
	}
}

func TestPageMissing(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/wiki/rest/api/content/99999")
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestPageComments(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/20001/child/comment?limit=100"))
	res := v["results"].([]any)
	if len(res) < 1 {
		t.Fatal("no comments")
	}
}

func TestPageCommentsEmpty(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/20001/child/comment?start=50"))
	res := v["results"].([]any)
	if res == nil {
		t.Fatal("null results")
	}
}

func TestUnsupported(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/dashboard")
	if res.StatusCode != 501 {
		t.Fatalf("status %d want 501", res.StatusCode)
	}
	v := decode(t, res)
	meta := v["issuetap"].(map[string]any)
	if meta["code"] != "unsupported_endpoint" {
		t.Fatalf("code=%v", meta["code"])
	}
}

func TestUnsupportedNot404(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/agile/1.0/board")
	if res.StatusCode == 404 {
		t.Fatal("known unimplemented route returned 404")
	}
	if res.StatusCode != 501 {
		t.Fatalf("status %d", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(raw), "unsupported_endpoint") {
		t.Fatalf("body=%s", raw)
	}
}

func TestWrites(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/issue/TAP-2/comment", map[string]any{
		"body": map[string]any{"type": "doc", "version": 1, "content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": "hello"},
			}},
		}},
	})
	if res.StatusCode != 201 {
		t.Fatalf("comment status %d", res.StatusCode)
	}
	res.Body.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/rest/api/3/issue/TAP-2/assignee",
		strings.NewReader(`{"accountId":"5b10a2844c20165700ede22g"}`))
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("assignee %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestWriteMissingIssue(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/issue/NOPE-1/comment", map[string]any{"body": "x"})
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestDCSearch(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.DC)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/2/search", map[string]any{
		"jql": "ORDER BY created ASC", "startAt": 0, "maxResults": 2,
	})
	v := decode(t, res)
	if int(v["total"].(float64)) != 3 {
		t.Fatalf("total=%v", v["total"])
	}
	if int(v["startAt"].(float64)) != 0 {
		t.Fatal("startAt")
	}
}

func TestDCWikiMarkupBody(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.DC)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/rest/api/2/issue/TAP-1"))
	fields := v["fields"].(map[string]any)
	desc, ok := fields["description"].(string)
	if !ok {
		t.Fatalf("DC description should be a string, got %T", fields["description"])
	}
	if !strings.Contains(desc, "flangewidget") {
		t.Fatalf("desc=%q", desc)
	}
}

func TestDeterminism(t *testing.T) {
	load := func() string {
		doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		st := store.New(store.Options{Seed: 1, Locale: locale.EN})
		if err := st.Apply(doc); err != nil {
			t.Fatal(err)
		}
		b, err := fixtures.MarshalYAML(st.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	a, b := load(), load()
	if a != b {
		t.Fatal("same fixture+seed produced different snapshots")
	}
}

func TestHealthz(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestJQLProjectFilter(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": `project = "TAP" ORDER BY updated DESC`, "maxResults": 100,
	})
	v := decode(t, res)
	if len(v["issues"].([]any)) != 3 {
		t.Fatalf("got %d", len(v["issues"].([]any)))
	}
}

func TestJQLKeyEquals(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": `key = "TAP-1"`, "fields": []string{"summary"},
	})
	v := decode(t, res)
	issues := v["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("len=%d", len(issues))
	}
	if issues[0].(map[string]any)["key"] != "TAP-1" {
		t.Fatal(issues[0])
	}
}

func TestAttachmentRedirect(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/rest/api/3/attachment/content/7001", nil)
	req.SetBasicAuth("you@example.com", "issuetap")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 302 {
		t.Fatalf("status %d", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if !strings.Contains(loc, "/file/") || !strings.Contains(loc, "name=crash.log") {
		t.Fatalf("Location=%s", loc)
	}
}

func TestSearchUpdatedFloor(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	// Incremental gadak JQL.
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql":        `updated >= "2026/08/02 09:58" ORDER BY updated ASC`,
		"maxResults": 100,
	})
	v := decode(t, res)
	if len(v["issues"].([]any)) < 1 {
		t.Fatal("expected issues updated after floor")
	}
}

func TestPutIssueUpdateLabelsAdd(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/rest/api/3/issue/TAP-2",
		strings.NewReader(`{"update":{"labels":[{"add":"smoke"}]}}`))
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("PUT status %d, want 204", res.StatusCode)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=labels"))
	fields := got["fields"].(map[string]any)
	raw, _ := fields["labels"].([]any)
	found := false
	for _, x := range raw {
		if x == "smoke" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GET TAP-2 labels=%v, want smoke after update.add", fields["labels"])
	}
}

func TestTruncateBytesCutsBody(t *testing.T) {
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(store.Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	s := api.New(cfg, st, faults.New([]faults.Fault{{
		Name: "cut", TruncateBytes: 20, PathPrefix: "/rest/api/3/myself",
	}}), nil, false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/myself")
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 20 {
		t.Fatalf("truncateBytes=20 left body len=%d", len(body))
	}
	if len(body) == 0 {
		t.Fatal("truncated body is empty")
	}
}
