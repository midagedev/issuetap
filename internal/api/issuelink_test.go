package api_test

// GDK-535: GET /issueLinkType + POST /issueLink. FAIL-first: current source
// returns unsupported_endpoint 501 for both (not a silent 404 — known Jira
// prefix, honest gap). After the routes land, these assertions are the
// contract: catalog shape, both-sides storage, 404/400 rejections,
// idempotent duplicate, persist reload.

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

func postIssueLink(t *testing.T, ts *httptest.Server, body map[string]any) *http.Response {
	t.Helper()
	return authPost(t, ts, "/rest/api/3/issueLink", body)
}

func issueLinkPayload(typeID, typeName, outwardKey, inwardKey string) map[string]any {
	typ := map[string]any{}
	if typeID != "" {
		typ["id"] = typeID
	}
	if typeName != "" {
		typ["name"] = typeName
	}
	out := map[string]any{}
	if outwardKey != "" {
		out["key"] = outwardKey
	}
	in := map[string]any{}
	if inwardKey != "" {
		in["key"] = inwardKey
	}
	return map[string]any{
		"type":         typ,
		"outwardIssue": out,
		"inwardIssue":  in,
	}
}

func issueLinksOf(t *testing.T, ts *httptest.Server, key string) []map[string]any {
	t.Helper()
	res := authGet(t, ts, "/rest/api/3/issue/"+key+"?fields=issuelinks")
	v := decode(t, res)
	fields, _ := v["fields"].(map[string]any)
	if fields == nil {
		t.Fatalf("%s: fields missing: %v", key, v)
	}
	raw, _ := fields["issuelinks"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s issuelinks[%d] is %T", key, i, item)
		}
		out = append(out, m)
	}
	return out
}

func directedLinkCount(links []map[string]any, typeName, side, otherKey string) int {
	n := 0
	for _, l := range links {
		typ, _ := l["type"].(map[string]any)
		if fmt.Sprint(typ["name"]) != typeName {
			continue
		}
		other, _ := l[side].(map[string]any)
		if other == nil {
			continue
		}
		if fmt.Sprint(other["key"]) == otherKey {
			n++
		}
	}
	return n
}

func jiraErrorMessages(t *testing.T, res *http.Response) (int, string) {
	t.Helper()
	status := res.StatusCode
	raw, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("status %d body %s: %v", status, raw, err)
	}
	msgs, _ := v["errorMessages"].([]any)
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, fmt.Sprint(m))
	}
	return status, strings.Join(parts, "\n")
}

func TestIssueLinkTypeCatalog(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := authGet(t, ts, "/rest/api/3/issueLinkType")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET issueLinkType status %d, want 200", res.StatusCode)
	}
	v := decode(t, res)
	raw, ok := v["issueLinkTypes"].([]any)
	if !ok {
		t.Fatalf("issueLinkTypes missing: %v", v)
	}
	want := []struct{ id, name, inward, outward string }{
		{"10000", "Blocks", "is blocked by", "blocks"},
		{"10001", "Cloners", "is cloned by", "clones"},
		{"10002", "Duplicate", "is duplicated by", "duplicates"},
		{"10003", "Relates", "relates to", "relates to"},
	}
	if len(raw) != len(want) {
		t.Fatalf("issueLinkTypes len=%d want %d: %v", len(raw), len(want), raw)
	}
	for i, w := range want {
		row, ok := raw[i].(map[string]any)
		if !ok {
			t.Fatalf("[%d] %T", i, raw[i])
		}
		if fmt.Sprint(row["id"]) != w.id || fmt.Sprint(row["name"]) != w.name {
			t.Errorf("[%d] id/name = %v/%v want %s/%s", i, row["id"], row["name"], w.id, w.name)
		}
		if fmt.Sprint(row["inward"]) != w.inward || fmt.Sprint(row["outward"]) != w.outward {
			t.Errorf("[%d] inward/outward = %v/%v want %s/%s", i, row["inward"], row["outward"], w.inward, w.outward)
		}
	}
}

func TestGetIssueLinksTypeHasCatalogIDInwardOutward(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("10000", "", "TAP-1", "TAP-3"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST issueLink status %d, want 201; errorMessages=%s", status, msgs)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	links := issueLinksOf(t, ts, "TAP-1")
	var typ map[string]any
	for _, l := range links {
		tmap, _ := l["type"].(map[string]any)
		if fmt.Sprint(tmap["name"]) == "Blocks" {
			typ = tmap
			break
		}
	}
	if typ == nil {
		t.Fatalf("TAP-1 missing Blocks link: %v", links)
	}
	if fmt.Sprint(typ["id"]) != "10000" {
		t.Fatalf("type.id=%v, want 10000", typ["id"])
	}
	if fmt.Sprint(typ["inward"]) != "is blocked by" {
		t.Fatalf("type.inward=%v, want is blocked by", typ["inward"])
	}
	if fmt.Sprint(typ["outward"]) != "blocks" {
		t.Fatalf("type.outward=%v, want blocks", typ["outward"])
	}
}

func TestPostIssueLinkStoresBothSides(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("10000", "", "TAP-1", "TAP-3"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST issueLink status %d, want 201; errorMessages=%s", status, msgs)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	// Real Jira labels each projection by the OTHER end's POST role
	// (measured on Cloud — gadak GDK-1204): the POSTed outwardIssue carries
	// an inwardIssue element and displays type.inward, and vice versa.
	a := issueLinksOf(t, ts, "TAP-1")
	b := issueLinksOf(t, ts, "TAP-3")
	if directedLinkCount(a, "Blocks", "inwardIssue", "TAP-3") != 1 {
		t.Fatalf("TAP-1 missing inwardIssue TAP-3 Blocks: %v", a)
	}
	if directedLinkCount(a, "Blocks", "outwardIssue", "TAP-3") != 0 {
		t.Fatalf("TAP-1 should not have outwardIssue TAP-3: %v", a)
	}
	if directedLinkCount(b, "Blocks", "outwardIssue", "TAP-1") != 1 {
		t.Fatalf("TAP-3 missing outwardIssue TAP-1 Blocks: %v", b)
	}
	if directedLinkCount(b, "Blocks", "inwardIssue", "TAP-1") != 0 {
		t.Fatalf("TAP-3 should not have inwardIssue TAP-1: %v", b)
	}
}

func TestPostIssueLinkByName(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("", "Blocks", "TAP-2", "TAP-3"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST by name status %d, want 201; errorMessages=%s", status, msgs)
	}
	res.Body.Close()

	a := issueLinksOf(t, ts, "TAP-2")
	b := issueLinksOf(t, ts, "TAP-3")
	if directedLinkCount(a, "Blocks", "inwardIssue", "TAP-3") != 1 {
		t.Fatalf("TAP-2 missing inwardIssue TAP-3: %v", a)
	}
	if directedLinkCount(b, "Blocks", "outwardIssue", "TAP-2") != 1 {
		t.Fatalf("TAP-3 missing outwardIssue TAP-2: %v", b)
	}
}

func TestPostIssueLinkAcceptsIssueIDs(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	body := map[string]any{
		"type":         map[string]any{"id": "10000"},
		"outwardIssue": map[string]any{"id": "10002"}, // TAP-2
		"inwardIssue":  map[string]any{"id": "10003"}, // TAP-3
	}
	res := postIssueLink(t, ts, body)
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST by issue id status %d, want 201; errorMessages=%s", status, msgs)
	}
	res.Body.Close()

	a := issueLinksOf(t, ts, "TAP-2")
	if directedLinkCount(a, "Blocks", "inwardIssue", "TAP-3") != 1 {
		t.Fatalf("id lookup did not store TAP-2→TAP-3: %v", a)
	}
}

func TestPostIssueLinkUnknownType404(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("99999", "", "TAP-1", "TAP-3"))
	status, msgs := jiraErrorMessages(t, res)
	if status != http.StatusNotFound {
		t.Fatalf("unknown type id status %d, want 404; errorMessages=%s", status, msgs)
	}
	if !strings.Contains(msgs, "No issue link type") {
		t.Fatalf("unknown type id errorMessages=%q, want 'No issue link type …'", msgs)
	}

	res = postIssueLink(t, ts, issueLinkPayload("", "NoSuchType", "TAP-1", "TAP-3"))
	status, msgs = jiraErrorMessages(t, res)
	if status != http.StatusNotFound {
		t.Fatalf("unknown type name status %d, want 404; errorMessages=%s", status, msgs)
	}
	if !strings.Contains(msgs, "No issue link type") {
		t.Fatalf("unknown type name errorMessages=%q, want 'No issue link type …'", msgs)
	}
}

func TestPostIssueLinkMissingIssue404(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("10000", "", "TAP-1", "NOPE-1"))
	status, msgs := jiraErrorMessages(t, res)
	if status != http.StatusNotFound {
		t.Fatalf("missing inward status %d, want 404; errorMessages=%s", status, msgs)
	}

	res = postIssueLink(t, ts, issueLinkPayload("10000", "", "NOPE-1", "TAP-1"))
	status, msgs = jiraErrorMessages(t, res)
	if status != http.StatusNotFound {
		t.Fatalf("missing outward status %d, want 404; errorMessages=%s", status, msgs)
	}
}

func TestPostIssueLinkSelf400(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := postIssueLink(t, ts, issueLinkPayload("10000", "", "TAP-1", "TAP-1"))
	status, msgs := jiraErrorMessages(t, res)
	if status != http.StatusBadRequest {
		t.Fatalf("self key status %d, want 400; errorMessages=%s", status, msgs)
	}

	body := map[string]any{
		"type":         map[string]any{"id": "10000"},
		"outwardIssue": map[string]any{"key": "TAP-1"},
		"inwardIssue":  map[string]any{"id": "10001"}, // TAP-1
	}
	res = postIssueLink(t, ts, body)
	status, msgs = jiraErrorMessages(t, res)
	if status != http.StatusBadRequest {
		t.Fatalf("self key/id status %d, want 400; errorMessages=%s", status, msgs)
	}
}

func TestPostIssueLinkIdempotentDoesNotDuplicate(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// TAP-1 already has Relates outward TAP-2 in tiny.yaml (one-sided).
	before := directedLinkCount(issueLinksOf(t, ts, "TAP-1"), "Relates", "outwardIssue", "TAP-2")
	if before != 1 {
		t.Fatalf("fixture TAP-1 Relates outward TAP-2 count=%d, want 1", before)
	}

	// The fixture element on TAP-1 names TAP-2 as outwardIssue, so in Jira
	// convention TAP-2 is the link's outward end — the heal POST says so.
	res := postIssueLink(t, ts, issueLinkPayload("10003", "", "TAP-2", "TAP-1"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("first duplicate POST status %d, want 201; errorMessages=%s", status, msgs)
	}
	res.Body.Close()
	res = postIssueLink(t, ts, issueLinkPayload("", "Relates", "TAP-2", "TAP-1"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("second duplicate POST status %d, want 201; errorMessages=%s", status, msgs)
	}
	res.Body.Close()

	a := issueLinksOf(t, ts, "TAP-1")
	if got := directedLinkCount(a, "Relates", "outwardIssue", "TAP-2"); got != 1 {
		t.Fatalf("TAP-1 Relates outward TAP-2 count=%d, want 1 (no duplicate): %v", got, a)
	}
	b := issueLinksOf(t, ts, "TAP-2")
	if directedLinkCount(b, "Relates", "inwardIssue", "TAP-1") != 1 {
		t.Fatalf("TAP-2 missing healed Relates inward TAP-1: %v", b)
	}
	if directedLinkCount(b, "Relates", "inwardIssue", "TAP-1") > 1 {
		t.Fatalf("TAP-2 duplicated Relates inward: %v", b)
	}
}

func TestPostIssueLinkSurvivesPersistReload(t *testing.T) {
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

	res := postIssueLink(t, ts, issueLinkPayload("10000", "", "TAP-1", "TAP-3"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST issueLink status %d, want 201; errorMessages=%s", status, msgs)
	}
	res.Body.Close()
	ts.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	a := st2.Issue("TAP-1")
	b := st2.Issue("TAP-3")
	if a == nil || b == nil {
		t.Fatal("issues lost across persist reload")
	}
	foundOut, foundIn := false, false
	for _, l := range a.Links {
		if l.TypeName == "Blocks" && l.InwardKey == "TAP-3" {
			foundOut = true
		}
	}
	for _, l := range b.Links {
		if l.TypeName == "Blocks" && l.OutwardKey == "TAP-1" {
			foundIn = true
		}
	}
	if !foundOut || !foundIn {
		t.Fatalf("link lost across persist reload; TAP-1 links=%+v TAP-3 links=%+v", a.Links, b.Links)
	}
}
