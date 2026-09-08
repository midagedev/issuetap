package api_test

// gadak GDK-1647: a comment posted by mistake had no way back. PUT and
// DELETE on /issue/{key}/comment/{id} are the two Cloud verbs that give one,
// and until they existed the only tracker gadak ships was add-only — which
// matters most for the caller most likely to post the wrong thing, an agent.

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

func commentPath(key, id string) string {
	return "/rest/api/3/issue/" + key + "/comment/" + id
}

// commentText reads the first text run out of a comment's ADF body.
func commentText(t *testing.T, c map[string]any) string {
	t.Helper()
	body, ok := c["body"].(map[string]any)
	if !ok {
		t.Fatalf("body missing or %T: %v", c["body"], c)
	}
	content, ok := body["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("body has no content: %v", body)
	}
	para, _ := content[0].(map[string]any)
	runs, ok := para["content"].([]any)
	if !ok || len(runs) == 0 {
		t.Fatalf("paragraph has no runs: %v", para)
	}
	run, _ := runs[0].(map[string]any)
	text, _ := run["text"].(string)
	return text
}

func postOneComment(t *testing.T, ts *httptest.Server, key, text string) map[string]any {
	t.Helper()
	status, created := postComment(t, ts, key, map[string]any{"body": commentADF(text)})
	if status != http.StatusCreated {
		t.Fatalf("post: status %d body=%v", status, created)
	}
	return created
}

func TestCommentEditReplacesBodyAndKeepsIdentity(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	created := postOneComment(t, ts, "TAP-2", "frist draft")
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no comment id: %v", created)
	}

	status, edited := decodeStatus(t, authPut(t, ts, commentPath("TAP-2", id),
		map[string]any{"body": commentADF("first draft")}))
	if status != http.StatusOK {
		t.Fatalf("put: status %d body=%v", status, edited)
	}
	if got := commentText(t, edited); got != "first draft" {
		t.Errorf("edited text=%q", got)
	}
	// The comment is the same comment: same id, same author, same Created.
	if edited["id"] != created["id"] {
		t.Errorf("id moved: %v → %v", created["id"], edited["id"])
	}
	if edited["created"] != created["created"] {
		t.Errorf("created moved: %v → %v", created["created"], edited["created"])
	}
	if edited["updated"] == created["updated"] {
		t.Errorf("updated did not move: %v", edited["updated"])
	}

	page := getComments(t, ts, "TAP-2")
	if len(page) != 1 {
		t.Fatalf("an edit added a comment: len=%d", len(page))
	}
	if got := commentText(t, page[0]); got != "first draft" {
		t.Errorf("GET after edit: text=%q", got)
	}
}

// The body normalizer has one owner, so an edit stores a plain-string body
// exactly as a post does. It is deliberately not wrapped into a document:
// the Jira Server dialect sends a comment as wiki markup, not ADF (gadak
// GDK-1637), and this asserts the parity, not a shape.
func TestCommentEditTreatsPlainTextLikeAPostDoes(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	const typed = "after, typed straight in"
	status, posted := postComment(t, ts, "TAP-2", map[string]any{"body": typed})
	if status != http.StatusCreated {
		t.Fatalf("post: status %d body=%v", status, posted)
	}

	created := postOneComment(t, ts, "TAP-2", "before")
	id, _ := created["id"].(string)
	status, edited := decodeStatus(t, authPut(t, ts, commentPath("TAP-2", id),
		map[string]any{"body": typed}))
	if status != http.StatusOK {
		t.Fatalf("put: status %d body=%v", status, edited)
	}
	if !reflect.DeepEqual(edited["body"], posted["body"]) {
		t.Errorf("edit stored %#v, a post stores %#v", edited["body"], posted["body"])
	}
}

// An edit moves the body and nothing else — visibility set at post time is
// still set afterwards.
func TestCommentEditKeepsVisibility(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	status, created := postComment(t, ts, "TAP-2", map[string]any{
		"body":       commentADF("restricted"),
		"visibility": map[string]any{"type": "role", "value": "Administrators"},
	})
	if status != http.StatusCreated {
		t.Fatalf("post: status %d body=%v", status, created)
	}
	id, _ := created["id"].(string)

	status, edited := decodeStatus(t, authPut(t, ts, commentPath("TAP-2", id),
		map[string]any{"body": commentADF("restricted, corrected")}))
	if status != http.StatusOK {
		t.Fatalf("put: status %d body=%v", status, edited)
	}
	vis := visibilityOf(t, edited)
	if vis["type"] != "role" || vis["value"] != "Administrators" {
		t.Errorf("edit dropped visibility: %v", vis)
	}
}

func TestCommentDeleteRemovesIt(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	created := postOneComment(t, ts, "TAP-2", "posted by mistake")
	id, _ := created["id"].(string)

	res := authDelete(t, ts, commentPath("TAP-2", id))
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d, want 204", res.StatusCode)
	}
	res.Body.Close()

	if page := getComments(t, ts, "TAP-2"); len(page) != 0 {
		t.Fatalf("comment survived the delete: %v", page)
	}
	// Deleting it twice is a 404, not a silent success: the second caller
	// has to be able to tell that nothing was there.
	res = authDelete(t, ts, commentPath("TAP-2", id))
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("second delete: status %d, want 404", res.StatusCode)
	}
	res.Body.Close()
}

func TestCommentEditAndDeleteRefuseUnknownIDs(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	cases := []struct{ name, path string }{
		{"unknown comment", commentPath("TAP-2", "9999999")},
		{"unknown issue", commentPath("NOPE-1", "9999999")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := decodeStatus(t, authPut(t, ts, tc.path,
				map[string]any{"body": commentADF("x")}))
			if status != http.StatusNotFound {
				t.Errorf("put: status %d, want 404 body=%v", status, body)
			}
			res := authDelete(t, ts, tc.path)
			if res.StatusCode != http.StatusNotFound {
				t.Errorf("delete: status %d, want 404", res.StatusCode)
			}
			res.Body.Close()
		})
	}
}
