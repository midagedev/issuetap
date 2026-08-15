package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

// Adversarial API self-review: three ways a consumer could get a
// wrong-but-plausible result. Each is blocked structurally.

func TestAdversarialUnparseableJQLDoesNotReturnAll(t *testing.T) {
	// Misuse: send garbage JQL and treat a 200 + every issue as "my filter works".
	// Block: 400.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{"jql": "not a real query !!!"})
	if res.StatusCode == 200 {
		t.Fatal("unparseable JQL returned 200 — would look like a working search")
	}
	res.Body.Close()
}

func TestAdversarialUnsupportedIsNotEmptySuccess(t *testing.T) {
	// Misuse: call GET /board, treat 404 or [] as "no boards".
	// Block: 501 + unsupported_endpoint.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/board")
	defer res.Body.Close()
	if res.StatusCode == 200 {
		t.Fatal("unimplemented route returned 200")
	}
	if res.StatusCode == 404 {
		t.Fatal("unimplemented route returned 404 — looks like a client bug")
	}
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v["issuetap"].(map[string]any)["code"] != "unsupported_endpoint" {
		t.Fatalf("%v", v)
	}
}

func TestAdversarialApproxCountMatchesSearch(t *testing.T) {
	// Misuse: trust approximate-count independently of search (a 0 would
	// look like an empty project).
	// Block: count == search length.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	c := decode(t, authPost(t, ts, "/rest/api/3/search/approximate-count", map[string]any{
		"jql": `project = "TAP"`,
	}))
	s := decode(t, authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": `project = "TAP"`, "maxResults": 100,
	}))
	n := int(c["count"].(float64))
	got := len(s["issues"].([]any))
	if n != got {
		t.Fatalf("count=%d search=%d", n, got)
	}
}

func TestAdversarialIsLastAndToken(t *testing.T) {
	// Misuse: isLast=true with a nextPageToken would make gadak stop early
	// or loop. Block: last page has isLast true and no token.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": "ORDER BY key ASC", "maxResults": 100,
	}))
	if v["isLast"] != true {
		t.Fatal("expected last page")
	}
	if tok, _ := v["nextPageToken"].(string); tok != "" {
		t.Fatalf("last page still has nextPageToken=%s", tok)
	}
}

func TestAdversarialChangelogTotal(t *testing.T) {
	// Misuse: total < len(values) can make gadak stop short; total=0 with
	// values can look like "no history".
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1/changelog"))
	vals := v["values"].([]any)
	if int(v["total"].(float64)) != len(vals) {
		t.Fatalf("total=%v len=%d", v["total"], len(vals))
	}
}

func TestSelfReviewUnknownFieldKeptInCustom(t *testing.T) {
	// Defect class: custom fields dropped on write.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "TAP"},
			"summary":   "custom field write",
			"issuetype": map[string]any{"id": "10003"},
			"customfield_10050": map[string]any{"value": "Sev1"},
		},
	})
	created := decode(t, res)
	key, _ := created["key"].(string)
	if key == "" {
		t.Fatalf("create: %v", created)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"?fields=*all"))
	fields := got["fields"].(map[string]any)
	if fields["customfield_10050"] == nil {
		t.Fatal("custom field dropped")
	}
}

func TestSelfReviewCommentTotalEqualsLenWhenUnpaged(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1"))
	fields := v["fields"].(map[string]any)
	c := fields["comment"].(map[string]any)
	cms := c["comments"].([]any)
	if int(c["total"].(float64)) != len(cms) {
		t.Fatalf("inline comment total=%v len=%d — gadak would extra-fetch", c["total"], len(cms))
	}
}

func TestSelfReviewXIssuetapHeader(t *testing.T) {
	// Defect class: someone points production at issuetap and cannot tell.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("X-Issuetap") != "1" {
		t.Fatal("missing X-Issuetap header")
	}
	info := decode(t, authGet(t, ts, "/rest/api/3/serverInfo"))
	if info["serverTitle"] != "issuetap" {
		t.Fatalf("serverTitle=%v", info["serverTitle"])
	}
}

func TestNameKeyedClientBreaksOnKorean(t *testing.T) {
	// The selling-point trap: JQL status = "In Progress" is 0 rows on ko.
	ts := testServer(t, locale.KO, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": `status = "In Progress"`, "maxResults": 100,
	}))
	if len(v["issues"].([]any)) != 0 {
		t.Fatal("status = In Progress should be empty on ko (name trap)")
	}
	v2 := decode(t, authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": `status = 3`, "maxResults": 100,
	}))
	if len(v2["issues"].([]any)) == 0 {
		t.Fatal("status = 3 (id) should match")
	}
}

func TestDCStartAtDriftSkipsARow(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.DC)
	defer ts.Close()
	// Arm drift via admin.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/faults", strings.NewReader(
		`{"faults":[{"name":"drift","drift":true,"pathContains":"/search"}]}`))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	page0 := decode(t, authPost(t, ts, "/rest/api/2/search", map[string]any{
		"jql": "ORDER BY key ASC", "startAt": 0, "maxResults": 2,
	}))
	page1 := decode(t, authPost(t, ts, "/rest/api/2/search", map[string]any{
		"jql": "ORDER BY key ASC", "startAt": 2, "maxResults": 2,
	}))
	// Without drift, startAt=2 returns TAP-3 only. With drift, start becomes 3
	// and the page is empty — the skipped key is the whole point.
	iss1 := page1["issues"].([]any)
	if len(page0["issues"].([]any)) != 2 {
		t.Fatalf("page0=%d", len(page0["issues"].([]any)))
	}
	if len(iss1) != 0 {
		t.Fatalf("drift should skip the last row, got %d", len(iss1))
	}
}

func TestHealthzNoAuth(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("admin should not require Atlassian auth, got %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestAdminUnknownPath(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/nope")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 404 {
		t.Fatalf("status %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestDCMyselfUsesUsername(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.DC)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/rest/api/2/myself"))
	if _, ok := v["accountId"]; ok {
		t.Fatal("DC myself should not advertise accountId as the identity")
	}
	if v["name"] == "" && v["key"] == "" {
		t.Fatalf("DC myself missing username/userKey: %v", v)
	}
}

func TestCQLCommentHitHasWebUI(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/wiki/rest/api/content/search?cql=type=comment"))
	res := v["results"].([]any)
	if len(res) == 0 {
		t.Fatal("expected comment hits")
	}
	hit := res[0].(map[string]any)
	links := hit["_links"].(map[string]any)
	webui, _ := links["webui"].(string)
	if !strings.Contains(webui, "/pages/") && !strings.Contains(webui, "pageId=") {
		t.Fatalf("comment webui %q cannot resolve container", webui)
	}
}
