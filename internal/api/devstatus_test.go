package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// gadak GDK-497: the dev-status surface serves Jira Cloud's internal payload
// shapes so one client reads Cloud and issuetap alike. Link → summary counts
// it → detail echoes it; a missing required param fails with Cloud's exact
// 500 {"message":"<param>"} shape (measured 2026-08-21).
func TestDevStatusLinkSummaryDetail(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// The issue's numeric id, the way a Cloud client would have it.
	issue := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2"))
	issueID, _ := issue["id"].(string)
	if issueID == "" {
		t.Fatal("TAP-2 has no id")
	}

	res := authPost(t, ts, "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID,
		"url":     "https://github.com/example/app/pull/7",
		"name":    "fix the drop",
		"status":  "merged",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("link status %d", res.StatusCode)
	}
	res.Body.Close()

	// Summary: the pullrequest block counts it; the other blocks stay zero.
	sum := decode(t, authGet(t, ts, "/rest/dev-status/latest/issue/summary?issueId="+issueID))
	pr := sum["summary"].(map[string]any)["pullrequest"].(map[string]any)["overall"].(map[string]any)
	if pr["count"].(float64) != 1 || pr["state"] != "MERGED" || pr["open"] != false {
		t.Fatalf("summary overall = %v", pr)
	}

	// Detail: required params enforced with Cloud's failure shape.
	res = authGet(t, ts, "/rest/dev-status/1.0/issue/detail?issueId="+issueID+"&dataType=pullrequest")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("missing applicationType: status %d, want Cloud's 500", res.StatusCode)
	}
	var perr map[string]any
	_ = json.NewDecoder(res.Body).Decode(&perr)
	res.Body.Close()
	if perr["message"] != "applicationType" {
		t.Fatalf("param error shape = %v", perr)
	}

	det := decode(t, authGet(t, ts,
		"/rest/dev-status/1.0/issue/detail?issueId="+issueID+"&applicationType=GitHub&dataType=pullrequest"))
	detail := det["detail"].([]any)
	if len(detail) != 1 {
		t.Fatalf("detail = %v", det)
	}
	prs := detail[0].(map[string]any)["pullRequests"].([]any)
	if len(prs) != 1 {
		t.Fatalf("pullRequests = %v", prs)
	}
	got := prs[0].(map[string]any)
	if got["url"] != "https://github.com/example/app/pull/7" || got["status"] != "MERGED" || got["name"] != "fix the drop" {
		t.Fatalf("pr = %v", got)
	}

	// Upsert by URL: same link again does not duplicate.
	res = authPost(t, ts, "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID, "url": "https://github.com/example/app/pull/7", "status": "declined",
	})
	res.Body.Close()
	sum = decode(t, authGet(t, ts, "/rest/dev-status/latest/issue/summary?issueId="+issueID))
	pr = sum["summary"].(map[string]any)["pullrequest"].(map[string]any)["overall"].(map[string]any)
	if pr["count"].(float64) != 1 {
		t.Fatalf("upsert duplicated: %v", pr)
	}

	// A rejected status never lands.
	res = authPost(t, ts, "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID, "url": "https://github.com/example/app/pull/9", "status": "sideways",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad status accepted: %d", res.StatusCode)
	}
	res.Body.Close()
}

// prRows fetches the pullRequests rows of a detail GET, failing the test on
// any envelope-shape deviation, so contract tests read rows not the envelope.
func prRows(t *testing.T, ts *httptest.Server, issueID string) []map[string]any {
	t.Helper()
	det := decode(t, authGet(t, ts,
		"/rest/dev-status/1.0/issue/detail?issueId="+issueID+"&applicationType=GitHub&dataType=pullrequest"))
	detail := det["detail"].([]any)
	if len(detail) != 1 {
		t.Fatalf("detail = %v", det)
	}
	raw, _ := detail[0].(map[string]any)["pullRequests"].([]any)
	rows := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, r.(map[string]any))
	}
	return rows
}

// detailPR is prRows for the single-link case.
func detailPR(t *testing.T, ts *httptest.Server, issueID string) map[string]any {
	t.Helper()
	rows := prRows(t, ts, issueID)
	if len(rows) != 1 {
		t.Fatalf("pullRequests = %v", rows)
	}
	return rows[0]
}

// gadak GDK-589: a dev link carries who opened the PR (author, a human
// login), where it heads (branch), and which agent wrote the link (actor).
// The actor is stamped by the server from the request identity — a client
// cannot forge it through the body. Re-POST is a partial update: empty body
// fields keep the stored values (an old client's re-POST must not erase
// author/branch), while the actor moves to the latest writer.
//
// | Contract | Test |
// | --- | --- |
// | POST body author/branch serve in detail as author{name} / source{branch} | TestDevLinkCarriesAuthorBranchActor |
// | actor is the request identity, never a body field | TestDevLinkCarriesAuthorBranchActor |
// | re-POST without author/branch keeps them; actor becomes the new writer | TestDevLinkCarriesAuthorBranchActor |
// | author/branch/actor survive a persist write + reload | TestDevLinkAuthorBranchActorSurvivePersistReload |
func TestDevLinkCarriesAuthorBranchActor(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	issue := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2"))
	issueID, _ := issue["id"].(string)
	if issueID == "" {
		t.Fatal("TAP-2 has no id")
	}

	// The 201 body already serves the stored shape.
	created := decode(t, actorPost(t, ts, "claude:354bff2b", "Claude (build 1)",
		"/rest/dev-status/1.0/issue/link", map[string]any{
			"issueId": issueID,
			"url":     "https://github.com/example/app/pull/21",
			"name":    "fix the drop",
			"status":  "open",
			"author":  "midagedev",
			"branch":  "gdk-589-dev-link-actor",
		}))
	if name := created["author"].(map[string]any)["name"]; name != "midagedev" {
		t.Fatalf("201 author = %v", created["author"])
	}
	if branch := created["source"].(map[string]any)["branch"]; branch != "gdk-589-dev-link-actor" {
		t.Fatalf("201 source = %v", created["source"])
	}
	actor := created["actor"].(map[string]any)
	if actor["accountId"] != "claude:354bff2b" || actor["displayName"] != "Claude (build 1)" {
		t.Fatalf("201 actor = %v", actor)
	}

	// Detail serves the Cloud vocabulary: author {name}, source {branch},
	// plus the issuetap extension actor {accountId, displayName}.
	got := detailPR(t, ts, issueID)
	if name := got["author"].(map[string]any)["name"]; name != "midagedev" {
		t.Fatalf("detail author = %v", got["author"])
	}
	if branch := got["source"].(map[string]any)["branch"]; branch != "gdk-589-dev-link-actor" {
		t.Fatalf("detail source = %v", got["source"])
	}
	actor = got["actor"].(map[string]any)
	if actor["accountId"] != "claude:354bff2b" || actor["displayName"] != "Claude (build 1)" {
		t.Fatalf("detail actor = %v", actor)
	}

	// A body actor field is ignored: without the actor header the link is
	// attributed to the Basic user, never to the forged accountId.
	res := actorPost(t, ts, "", "", "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID,
		"url":     "https://github.com/example/app/pull/22",
		"actor":   map[string]any{"accountId": "forged:slug", "displayName": "Forged"},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("forged-actor link status %d", res.StatusCode)
	}
	var forged map[string]any
	for _, raw := range prRows(t, ts, issueID) {
		if raw["url"] == "https://github.com/example/app/pull/22" {
			forged = raw
		}
	}
	if forged == nil {
		t.Fatal("pull/22 missing after forged-actor POST")
	}
	if a := forged["actor"].(map[string]any); a["accountId"] != "5b10a2844c20165700ede21g" {
		t.Fatalf("body actor was honored: %v, want the Basic user Ada", a)
	}

	// Partial update: re-POST pull/21 without author/branch keeps them, the
	// actor moves to the latest writer, and the pre-existing name-keep rule
	// still holds.
	res = actorPost(t, ts, "grok:tars", "", "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID, "url": "https://github.com/example/app/pull/21", "status": "merged",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("re-POST status %d", res.StatusCode)
	}
	res.Body.Close()
	var row map[string]any
	for _, raw := range prRows(t, ts, issueID) {
		if raw["url"] == "https://github.com/example/app/pull/21" {
			row = raw
		}
	}
	if row == nil {
		t.Fatal("pull/21 missing after re-POST")
	}
	if name := row["author"].(map[string]any)["name"]; name != "midagedev" {
		t.Fatalf("re-POST erased author: %v", row["author"])
	}
	if branch := row["source"].(map[string]any)["branch"]; branch != "gdk-589-dev-link-actor" {
		t.Fatalf("re-POST erased branch: %v", row["source"])
	}
	if row["name"] != "fix the drop" {
		t.Fatalf("re-POST erased name: %v", row["name"])
	}
	actor = row["actor"].(map[string]any)
	if actor["accountId"] != "grok:tars" || actor["displayName"] != "grok:tars" {
		t.Fatalf("re-POST actor = %v, want the latest writer grok:tars", actor)
	}
	if row["status"] != "MERGED" {
		t.Fatalf("re-POST status = %v", row["status"])
	}
}

// TestDevLinkAuthorBranchActorSurvivePersistReload: the dev-link fields
// survive a persist write + reload — fixtures.DevPR must carry them or the
// link loses its author/branch/actor on restart (the same trap GDK-588 hit
// with User.AccountType).
func TestDevLinkAuthorBranchActorSurvivePersistReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	// openPersisted starts a server on the persistence file. seed=true
	// applies the fixture (first run); the reload run seeds nothing —
	// re-applying would overwrite TAP-2 and erase the persisted link.
	// (Same closure shape as actor_test.go: it is local to that test, so
	// this is a sibling copy rather than a shared helper.)
	openPersisted := func(seed bool) *httptest.Server {
		st, err := store.Open(store.Options{
			Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if seed {
			doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Apply(doc); err != nil {
				t.Fatal(err)
			}
		}
		cfg := config.Default()
		cfg.Dialect.Kind = dialect.Cloud
		ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
		t.Cleanup(ts.Close)
		return ts
	}

	ts1 := openPersisted(true)
	issue := decode(t, authGet(t, ts1, "/rest/api/3/issue/TAP-2"))
	issueID, _ := issue["id"].(string)
	res := actorPost(t, ts1, "claude:354bff2b", "", "/rest/dev-status/1.0/issue/link", map[string]any{
		"issueId": issueID,
		"url":     "https://github.com/example/app/pull/31",
		"author":  "midagedev",
		"branch":  "gdk-589-persist",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("link status %d", res.StatusCode)
	}
	res.Body.Close()

	ts2 := openPersisted(false) // re-opens the persisted file
	got := detailPR(t, ts2, issueID)
	if name := got["author"].(map[string]any)["name"]; name != "midagedev" {
		t.Fatalf("post-reload author = %v (lost on the persist round trip)", got["author"])
	}
	if branch := got["source"].(map[string]any)["branch"]; branch != "gdk-589-persist" {
		t.Fatalf("post-reload source = %v (lost on the persist round trip)", got["source"])
	}
	actor := got["actor"].(map[string]any)
	if actor["accountId"] != "claude:354bff2b" || actor["displayName"] != "claude:354bff2b" {
		t.Fatalf("post-reload actor = %v (lost on the persist round trip)", actor)
	}
}

func TestDevStatusUnknownSubpathIs501(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/dev-status/1.0/branch")
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Fatalf("unsupported dev-status subpath returned 404; body=%s", raw)
	}
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d want 501 body=%s", res.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "unsupported_endpoint") {
		t.Fatalf("body=%s", raw)
	}
}

func TestCompatibilityInventoryIncludesRecentRoutes(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/compatibility")
	if err != nil {
		t.Fatal(err)
	}
	v := decode(t, res)
	raw, _ := v["routes"].([]any)
	got := map[string]bool{}
	for _, item := range raw {
		row, _ := item.(map[string]any)
		got[fmt.Sprint(row["Method"])+" "+fmt.Sprint(row["Path"])] = true
	}
	want := []string{
		"POST /rest/api/{v}/project",
		"GET /rest/dev-status/{v}/issue/summary",
		"GET /rest/dev-status/{v}/issue/detail",
		"POST /rest/dev-status/{v}/issue/link",
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("GET /api/compatibility missing %s", w)
		}
	}
}
