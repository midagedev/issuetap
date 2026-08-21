package api_test

// POST /issue/{key}/claim over HTTP (gadak GDK-591). The store rules are
// covered in internal/store/claim_test.go; these tests pin the wire
// contract: status codes, the jiraError envelope on 409, the actor taken
// from X-Issuetap-Actor (never the body), and the response shape
// {key, assignee, status, claimedAt}.
//
// | Contract | Test |
// | --- | --- |
// | unclaimed issue: 200, actor is assignee, status in-progress, claimedAt | TestClaimHTTPRequest |
// | another actor's hold: 409 errorMessages names the holder | TestClaimHTTP409NamesHolder |
// | same actor: 200 idempotent, changelog unchanged, claimedAt from changelog | TestClaimHTTPIdempotent |
// | takeOver: 200, assignee reassigned, no re-transition | TestClaimHTTPTakeOver |
// | no in-progress destination: 400 errorMessages | TestClaimHTTPNoInProgressTransition400 |
// | body actor fields are ignored; identity is the header | TestClaimHTTPActorIsHeaderNotBody |
// | unknown issue: 404 | TestClaimHTTPMissingIssue404 |

import (
	"net/http"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func claimBody(extra map[string]any) map[string]any {
	body := map[string]any{}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestClaimHTTPRequest(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := actorPost(t, ts, "claude:354bff2b", "Claude", "/rest/api/3/issue/TAP-2/claim", claimBody(nil))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("claim status %d, want 200", res.StatusCode)
	}
	v := decode(t, res)
	if v["key"] != "TAP-2" {
		t.Fatalf("key=%v", v["key"])
	}
	assignee := v["assignee"].(map[string]any)
	if assignee["accountId"] != "claude:354bff2b" || assignee["displayName"] != "Claude" {
		t.Fatalf("assignee=%v, want the request actor", assignee)
	}
	st := v["status"].(map[string]any)
	cat := st["statusCategory"].(map[string]any)
	if st["id"] != "3" || cat["key"] != "indeterminate" {
		t.Fatalf("status=%v, want id 3 / in-progress category", st)
	}
	if v["claimedAt"] == nil || v["claimedAt"] == "" {
		t.Fatalf("claimedAt missing: %v", v)
	}

	// Read-back: the graph carries the claim.
	issue := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-2?fields=status,assignee"))
	fields := issue["fields"].(map[string]any)
	if fields["assignee"].(map[string]any)["accountId"] != "claude:354bff2b" {
		t.Fatalf("stored assignee=%v", fields["assignee"])
	}
	cl := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-2/changelog"))
	for _, raw := range cl["values"].([]any) {
		row := raw.(map[string]any)
		if row["author"].(map[string]any)["accountId"] != "claude:354bff2b" {
			t.Fatalf("changelog row author=%v, want the actor", row["author"])
		}
	}
}

func TestClaimHTTP409NamesHolder(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	// TAP-1 is In Progress, assigned to Dana.
	res := actorPost(t, ts, "grok:tars", "Grok", "/rest/api/3/issue/TAP-1/claim", claimBody(nil))
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("claim status %d, want 409", res.StatusCode)
	}
	v := decode(t, res)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 || msgs[0] != "TAP-1 is already claimed by Dana" {
		t.Fatalf("errorMessages=%v, want the holder named", msgs)
	}
}

func TestClaimHTTPIdempotent(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	// Dana already holds TAP-1; the actor slug is her accountId.
	res := actorPost(t, ts, "5b10a2844c20165700ede22g", "", "/rest/api/3/issue/TAP-1/claim", claimBody(nil))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("re-claim status %d, want 200", res.StatusCode)
	}
	v := decode(t, res)
	if v["claimedAt"] != "2026-07-20T10:00:00.000+0900" {
		t.Fatalf("claimedAt=%v, want the changelog entry into In Progress", v["claimedAt"])
	}
	cl := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-1/changelog"))
	if got := cl["total"].(float64); got != 3 {
		t.Fatalf("changelog total=%v, want the 3 fixture rows (no duplicates)", got)
	}
}

func TestClaimHTTPTakeOver(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := actorPost(t, ts, "claude:354bff2b", "Claude", "/rest/api/3/issue/TAP-1/claim", claimBody(map[string]any{"takeOver": true}))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("takeOver status %d, want 200", res.StatusCode)
	}
	v := decode(t, res)
	if v["assignee"].(map[string]any)["accountId"] != "claude:354bff2b" {
		t.Fatalf("assignee=%v, want the taking-over actor", v["assignee"])
	}
	if v["status"].(map[string]any)["id"] != "3" {
		t.Fatalf("status=%v, want 3 unchanged", v["status"])
	}
	// The takeover trace: one new assignee row Dana → Claude.
	cl := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-1/changelog"))
	vals := cl["values"].([]any)
	if len(vals) != 4 {
		t.Fatalf("changelog rows=%d, want 4 (3 fixture + takeover)", len(vals))
	}
	last := vals[len(vals)-1].(map[string]any)
	item := last["items"].([]any)[0].(map[string]any)
	if item["fieldId"] != "assignee" || item["fromString"] != "Dana" || item["toString"] != "Claude" {
		t.Fatalf("takeover item=%v, want assignee Dana→Claude", item)
	}
}

func TestClaimHTTPNoInProgressTransition400(t *testing.T) {
	doc := loadExampleDoc(t, "tiny.yaml")
	// Overwrite the only in-progress status to new: no destination qualifies.
	doc.Statuses = append(doc.Statuses, fixtures.Status{ID: "3", Name: "Blocked", Category: "new"})
	ts := testServerDoc(t, doc, locale.EN)
	defer ts.Close()
	res := actorPost(t, ts, "claude:354bff2b", "", "/rest/api/3/issue/TAP-2/claim", claimBody(nil))
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("claim status %d, want 400", res.StatusCode)
	}
	v := decode(t, res)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 || msgs[0] != "no in-progress transition available" {
		t.Fatalf("errorMessages=%v", msgs)
	}
}

func TestClaimHTTPActorIsHeaderNotBody(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	// A body that tries to name the assignee must not win: the actor header
	// (here: none → the Basic user Ada) is the identity.
	res := actorPost(t, ts, "", "", "/rest/api/3/issue/TAP-2/claim", map[string]any{
		"assignee": map[string]any{"accountId": "5b10a2844c20165700ede22g"},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("claim status %d, want 200", res.StatusCode)
	}
	v := decode(t, res)
	if got := v["assignee"].(map[string]any)["accountId"]; got != "5b10a2844c20165700ede21g" {
		t.Fatalf("assignee=%v, want the Basic user Ada (body assignee ignored)", got)
	}
}

func TestClaimHTTPMissingIssue404(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := actorPost(t, ts, "claude:354bff2b", "", "/rest/api/3/issue/TAP-999/claim", claimBody(nil))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("claim status %d, want 404", res.StatusCode)
	}
	v := decode(t, res)
	if len(errorMessages(t, v)) == 0 {
		t.Fatalf("body=%v, want errorMessages", v)
	}
}
