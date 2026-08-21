package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

// GDK-370: a 501 for a near-miss implemented path should name that path.
// Wrong suggestions are worse than none, so distant paths stay byte-identical
// to the pre-hint envelope.

func TestUnsupportedSuggestsUserSearchSibling(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/users/search")
	raw, v := readJSONBody(t, res)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d want 501 body=%s", res.StatusCode, raw)
	}
	assertUnsupportedCode(t, v, raw)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 {
		t.Fatalf("errorMessages=%v", msgs)
	}
	want := "issuetap does not implement GET /rest/api/3/users/search; did you mean GET /rest/api/3/user/search?"
	if msgs[0] != want {
		t.Fatalf("errorMessages[0]=%q want %q", msgs[0], want)
	}
	if !strings.Contains(msgs[0], "user/search") {
		t.Fatalf("missing user/search hint in %q", msgs[0])
	}
	meta := issuetapMetaMap(t, v)
	if meta["suggest"] != "GET /rest/api/3/user/search" {
		t.Fatalf("issuetap.suggest=%v", meta["suggest"])
	}
}

func TestUnsupportedDistantPathHasNoSuggestion(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/zzz/qqq")
	raw, v := readJSONBody(t, res)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d want 501 body=%s", res.StatusCode, raw)
	}
	assertUnsupportedCode(t, v, raw)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 {
		t.Fatalf("errorMessages=%v", msgs)
	}
	want := "issuetap does not implement GET /rest/api/3/zzz/qqq"
	if msgs[0] != want {
		t.Fatalf("errorMessages[0]=%q want %q (must stay the pre-hint sentence)", msgs[0], want)
	}
	if strings.Contains(msgs[0], "did you mean") {
		t.Fatalf("distant path must not hint: %q", msgs[0])
	}
	meta := issuetapMetaMap(t, v)
	if _, ok := meta["suggest"]; ok {
		t.Fatalf("issuetap.suggest present on a distant path: %v body=%s", meta["suggest"], raw)
	}
}

func TestUnsupportedSuggestsImplementedMethod(t *testing.T) {
	// Inventory has POST /rest/api/{v}/search/jql (Supported) and no GET.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/search/jql")
	raw, v := readJSONBody(t, res)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d want 501 body=%s", res.StatusCode, raw)
	}
	assertUnsupportedCode(t, v, raw)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 {
		t.Fatalf("errorMessages=%v", msgs)
	}
	want := "issuetap does not implement GET /rest/api/3/search/jql; did you mean POST /rest/api/3/search/jql?"
	if msgs[0] != want {
		t.Fatalf("errorMessages[0]=%q want %q", msgs[0], want)
	}
	meta := issuetapMetaMap(t, v)
	if meta["suggest"] != "POST /rest/api/3/search/jql" {
		t.Fatalf("issuetap.suggest=%v", meta["suggest"])
	}
}

func TestUnsupportedDashboardMessageUnchanged(t *testing.T) {
	// COMPATIBILITY.md documents this exact envelope. A hint here would
	// silently drift that example; dashboard has no distance-1 sibling.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/dashboard")
	raw, v := readJSONBody(t, res)
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status %d want 501 body=%s", res.StatusCode, raw)
	}
	assertUnsupportedCode(t, v, raw)
	msgs := errorMessages(t, v)
	if len(msgs) != 1 || msgs[0] != "issuetap does not implement GET /rest/api/3/dashboard" {
		t.Fatalf("errorMessages=%v", msgs)
	}
	meta := issuetapMetaMap(t, v)
	if _, ok := meta["suggest"]; ok {
		t.Fatalf("dashboard 501 grew a suggest field: %v", meta["suggest"])
	}
}

func readJSONBody(t *testing.T, res *http.Response) (string, map[string]any) {
	t.Helper()
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("json: %v body=%s", err, raw)
	}
	return string(raw), v
}

func errorMessages(t *testing.T, v map[string]any) []string {
	t.Helper()
	raw, _ := v["errorMessages"].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func issuetapMetaMap(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	meta, _ := v["issuetap"].(map[string]any)
	if meta == nil {
		t.Fatalf("missing issuetap meta: %v", v)
	}
	return meta
}

func assertUnsupportedCode(t *testing.T, v map[string]any, raw string) {
	t.Helper()
	errs, _ := v["errors"].(map[string]any)
	if errs["endpoint"] != "unsupported_endpoint" {
		t.Fatalf("errors.endpoint=%v body=%s", errs["endpoint"], raw)
	}
	meta := issuetapMetaMap(t, v)
	if meta["code"] != "unsupported_endpoint" {
		t.Fatalf("issuetap.code=%v body=%s", meta["code"], raw)
	}
}
