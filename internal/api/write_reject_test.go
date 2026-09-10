package api_test

// HTTP gates for the write-rejection contract (GDK-1211, GDK-1219): the
// 400s a Cloud client expects, over the same server every contract test
// uses. A rejected write must also be a *no-op* — the client's next GET
// sees the issue exactly as it was, and the changelog has no row.

import (
	"net/http"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

// TestPostIssueUnknownProjectIs400: POST /issue naming an unknown project
// must answer 400 with an errors.project map — it used to answer 201 and
// bootstrap an empty project named after its own key, a project no
// admin ever created. And nothing may exist afterwards.
func TestPostIssueUnknownProjectIs400(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "NOPE"},
			"summary":   "must not bootstrap",
			"issuetype": map[string]any{"id": "10003"},
		},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /issue unknown project: status %d, want 400", res.StatusCode)
	}
	v := decode(t, res)
	errs, _ := v["errors"].(map[string]any)
	if errs["project"] == nil {
		t.Fatalf("400 body missing errors.project: %v", v)
	}

	// The rejection created nothing — GET /project/NOPE is a 404, not a
	// real project the admin never asked for.
	res2 := authGet(t, ts, "/rest/api/3/project/NOPE")
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /project/NOPE after rejected create: status %d, want 404",
			res2.StatusCode)
	}
}

// TestPostIssueUnknownIssueTypeIs400: an issuetype id outside the catalog
// is 400 "The issue type selected is invalid." at the HTTP layer too —
// the store-level FieldError must surface through the field-error map.
func TestPostIssueUnknownIssueTypeIs400(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "TAP"},
			"summary":   "bad type",
			"issuetype": map[string]any{"id": "99999"},
		},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /issue unknown issuetype: status %d, want 400", res.StatusCode)
	}
	v := decode(t, res)
	errs, _ := v["errors"].(map[string]any)
	if errs["issuetype"] == nil {
		t.Fatalf("400 body missing errors.issuetype: %v", v)
	}
}

// TestMixedPut400LeavesIssueUnchanged: the GDK-1219 scenario end to end —
// PUT /issue with a valid summary and an invalid fixVersions id answers
// 400, and the very next GET sees the issue byte-for-byte as before (no
// summary change, no updated bump, no changelog row).
func TestMixedPut400LeavesIssueUnchanged(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	before := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=summary,updated"))
	beforeFields := before["fields"].(map[string]any)
	histBefore := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2/changelog"))

	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"summary":     "should never land",
			"fixVersions": []any{map[string]any{"id": "nonexistent-id"}},
		},
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("mixed PUT: status %d, want 400", res.StatusCode)
	}

	after := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=summary,updated"))
	afterFields := after["fields"].(map[string]any)
	if afterFields["summary"] != beforeFields["summary"] {
		t.Fatalf("summary %v → %v — earlier field survived the failed PUT",
			beforeFields["summary"], afterFields["summary"])
	}
	if afterFields["updated"] != beforeFields["updated"] {
		t.Fatalf("updated %v → %v — failed PUT bumped the timestamp",
			beforeFields["updated"], afterFields["updated"])
	}
	histAfter := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2/changelog"))
	if histAfter["total"] != histBefore["total"] {
		t.Fatalf("changelog total %v → %v — failed PUT recorded history",
			histBefore["total"], histAfter["total"])
	}
}
