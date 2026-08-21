package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
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
