package conformance

// GDK-1213: conformance used to be read-sync only, and GDK-1207/1208/1209
// survived precisely in that uncovered write surface. This file drives
// gadak's real write verbs — edit, comment, assign, transition, attach,
// link/unlink, page create — against a live issuetap and asserts the
// Cloud contract on the origin after every write (GET + changelog, and
// the 400 rejections a Cloud client relies on). Gadak's own tests hit
// httptest fakes; only real gadak over real HTTP proves the two sides
// agree.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/midagedev/issuetap/internal/locale"
)

// gadakRunner runs one gadak invocation against home, failing the test on
// a non-zero exit.
func gadakRunner(t *testing.T, bin, home string) func(args ...string) string {
	t.Helper()
	return func(args ...string) string {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "GADAK_HOME="+home, "GADAK_PROFILE=")
		out, err := cmd.CombinedOutput()
		t.Logf("gadak %s:\n%s", strings.Join(args, " "), out)
		if err != nil {
			t.Fatalf("gadak %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
}

// originJSON GETs path with lab credentials and decodes the JSON body.
func originJSON(t *testing.T, base, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("you@example.com", "issuetap")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("GET %s decode: %v", path, err)
	}
	return res.StatusCode, v
}

// issueFields GETs one issue's requested fields.
func issueFields(t *testing.T, base, key, fields string) map[string]any {
	t.Helper()
	code, v := originJSON(t, base, "/rest/api/3/issue/"+key+"?fields="+url.QueryEscape(fields))
	if code != http.StatusOK {
		t.Fatalf("GET %s: status %d body %v", key, code, v)
	}
	f, _ := v["fields"].(map[string]any)
	return f
}

// changelogHasItem reports whether any changelog row for key carries an
// item with the given field and toString.
func changelogHasItem(t *testing.T, base, key, field, toString string) bool {
	t.Helper()
	code, v := originJSON(t, base, "/rest/api/3/issue/"+key+"/changelog")
	if code != http.StatusOK {
		t.Fatalf("GET %s changelog: status %d body %v", key, code, v)
	}
	values, _ := v["values"].([]any)
	for _, row := range values {
		m, _ := row.(map[string]any)
		items, _ := m["items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			if im["field"] == field && im["toString"] == toString {
				return true
			}
		}
	}
	return false
}

// originPOST sends a JSON POST with lab credentials.
func originPOST(t *testing.T, base, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("POST %s decode: %v", path, err)
	}
	return res.StatusCode, v
}

// originPUT sends a JSON PUT with lab credentials.
func originPUT(t *testing.T, base, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, base+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("PUT %s decode: %v", path, err)
	}
	return res.StatusCode, v
}

// TestGadakWriteVerbsRoundTrip: every write verb runs for real and the
// origin answers with the Cloud shape afterwards. Each step asserts on
// the origin (GET/changelog), not on gadak's own output — the contract
// under test is issuetap's, the verb is just the vehicle a real client
// would use.
func TestGadakWriteVerbsRoundTrip(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)
	home := writeGadakHome(t, base)
	run := gadakRunner(t, bin, home)

	run("sync", "--full")

	// edit: summary + label land, and the change is recorded.
	run("edit", "TAP-2", "--summary", "Edited by conformance", "--label", "+conf")
	f := issueFields(t, base, "TAP-2", "summary,labels")
	if f["summary"] != "Edited by conformance" {
		t.Fatalf("edit summary: %v", f["summary"])
	}
	if !strings.Contains(fmt.Sprint(f["labels"]), "conf") {
		t.Fatalf("edit labels: %v", f["labels"])
	}
	if !changelogHasItem(t, base, "TAP-2", "summary", "Edited by conformance") {
		t.Fatal("edit left no summary changelog row (GDK-1208 class)")
	}

	// comment: the body survives the round trip inside the comment feed.
	run("comment", "TAP-2", "-m", "Conformance comment")
	f = issueFields(t, base, "TAP-2", "comment")
	feed, _ := f["comment"].(map[string]any)
	raw, _ := json.Marshal(feed)
	if !strings.Contains(string(raw), "Conformance comment") {
		t.Fatalf("comment body missing from feed: %s", raw)
	}

	// assign: by display name, resolved to an accountId on the origin.
	run("assign", "TAP-2", "Dana")
	f = issueFields(t, base, "TAP-2", "assignee")
	assignee, _ := f["assignee"].(map[string]any)
	if assignee == nil || assignee["displayName"] != "Dana" {
		t.Fatalf("assign: %v", f["assignee"])
	}

	// transition: by name, reflected in status + changelog.
	run("transition", "TAP-2", "In Progress")
	f = issueFields(t, base, "TAP-2", "status")
	status, _ := f["status"].(map[string]any)
	if status == nil || status["name"] != "In Progress" {
		t.Fatalf("transition status: %v", f["status"])
	}
	if !changelogHasItem(t, base, "TAP-2", "status", "In Progress") {
		t.Fatal("transition left no status changelog row (GDK-1208 class)")
	}

	// attach: a real file becomes a real attachment row.
	dir := t.TempDir()
	note := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(note, []byte("attachment payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("attach", "TAP-2", note)
	f = issueFields(t, base, "TAP-2", "attachment")
	raw, _ = json.Marshal(f["attachment"])
	if !strings.Contains(string(raw), "note.txt") {
		t.Fatalf("attachment missing: %s", raw)
	}

	// link then unlink: the DELETE side of issueLink (GDK-1206 axis B
	// sibling) — the link appears with Cloud direction, then is gone.
	run("link", "TAP-1", "TAP-2", "--type", "blocks")
	links := issueLinksJSON(t, base, "TAP-1")
	if !hasJSONDirectedLink(links, "Blocks", "outwardIssue", "TAP-2") {
		t.Fatalf("link missing after gadak link: %v", links)
	}
	run("unlink", "TAP-1", "TAP-2", "--type", "blocks")
	links = issueLinksJSON(t, base, "TAP-1")
	if hasJSONDirectedLink(links, "Blocks", "outwardIssue", "TAP-2") {
		t.Fatalf("link survived gadak unlink: %v", links)
	}

	// page create: the wiki write surface — a new page is searchable.
	run("page", "create", "--space", "DOCS", "--title", "Conformance created page", "-m", "created body")
	q := url.Values{}
	q.Set("cql", `space="DOCS" and type=page`)
	code, v := originJSON(t, base, "/wiki/rest/api/content/search?"+q.Encode())
	if code != http.StatusOK {
		t.Fatalf("wiki search: %d %v", code, v)
	}
	raw, _ = json.Marshal(v["results"])
	if !strings.Contains(string(raw), "Conformance created page") {
		t.Fatalf("created page not searchable: %s", raw)
	}
}

// TestWriteRejectsLikeCloud: the 400 half of the write contract, on the
// same live server the verbs above write to. gadak's CLI pre-resolves
// projects and types, so it cannot carry these invalid payloads — the
// axes a hostile or version-skewed client would hit. These were the
// GDK-1211/GDK-1219 defects: the server used to accept all three.
func TestWriteRejectsLikeCloud(t *testing.T) {
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)

	// Unknown project: 400 naming the field, and no project bootstrapped.
	code, v := originPOST(t, base, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "NOPE"},
			"summary":   "must not bootstrap",
			"issuetype": map[string]any{"id": "10003"},
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown project: status %d body %v", code, v)
	}
	errs, _ := v["errors"].(map[string]any)
	if errs["project"] == nil {
		t.Fatalf("unknown project: no errors.project in %v", v)
	}
	if sc, _ := originJSON(t, base, "/rest/api/3/project/NOPE"); sc != http.StatusNotFound {
		t.Fatalf("project NOPE exists after rejected create: status %d", sc)
	}

	// Unknown issuetype: 400, never a silently filed dangling id.
	code, v = originPOST(t, base, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "TAP"},
			"summary":   "bad type",
			"issuetype": map[string]any{"id": "99999"},
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown issuetype: status %d body %v", code, v)
	}
	errs, _ = v["errors"].(map[string]any)
	if errs["issuetype"] == nil {
		t.Fatalf("unknown issuetype: no errors.issuetype in %v", v)
	}

	// Mixed PUT (GDK-1219): a valid summary beside an invalid issuetype
	// is all-or-nothing — 400, and the issue is unchanged afterwards.
	before := issueFields(t, base, "TAP-2", "summary,updated,status")
	code, v = originPUT(t, base, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"summary":   "must never land",
			"issuetype": map[string]any{"id": "99999"},
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("mixed PUT: status %d body %v", code, v)
	}
	after := issueFields(t, base, "TAP-2", "summary,updated,status")
	if after["summary"] != before["summary"] || after["updated"] != before["updated"] {
		t.Fatalf("rejected PUT mutated the issue: before=%v after=%v", before, after)
	}
}

// TestWikiPaginationCloudContract: the GDK-1210 axes over a fixture with
// enough pages to paginate — `_links.next` percent-encodes the cql like
// Cloud (a raw concatenation corrupts the query for any client that
// re-parses it), and `order by lastmodified desc` serves newest-first.
// The gadak half is a compatibility pin: real gadak paginates wiki search
// at limit=50 and follows the encoded next link across the boundary.
func TestWikiPaginationCloudContract(t *testing.T) {
	root := repoRoot(t)
	fixtureSrc, err := os.ReadFile(filepath.Join(root, "examples/fixtures/tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// 52 pages total: gadak's collector (limit 50) must cross one next
	// link to see them all.
	var b strings.Builder
	b.WriteString("pages:\n")
	for i := 0; i < 51; i++ {
		fmt.Fprintf(&b, "  - id: \"%d\"\n    title: Derived page %d\n    space: DOCS\n"+
			"    version: 1\n    when: \"2026-08-10T00:%02d:00.000Z\"\n"+
			"    author: 5b10a2844c20165700ede21g\n    body: derived page %d\n",
			20010+i, i, i, i)
	}
	derived := strings.Replace(string(fixtureSrc), "pages:\n", b.String(), 1)
	fixture := filepath.Join(t.TempDir(), "wiki-pages.yaml")
	if err := os.WriteFile(fixture, []byte(derived), 0o600); err != nil {
		t.Fatal(err)
	}

	base, _ := startIssuetap(t, fixture, locale.EN, nil)

	// next link: encoded, and the cql round-trips through a re-parse.
	const cql = `space="DOCS" and type=page`
	q := url.Values{}
	q.Set("cql", cql)
	q.Set("limit", "25")
	code, v := originJSON(t, base, "/wiki/rest/api/content/search?"+q.Encode())
	if code != http.StatusOK {
		t.Fatalf("wiki search: %d %v", code, v)
	}
	links, _ := v["_links"].(map[string]any)
	next, _ := links["next"].(string)
	if next == "" {
		t.Fatalf("no _links.next with 52 pages and limit=25: %v", links)
	}
	if strings.ContainsAny(next, " \"") {
		t.Fatalf("_links.next not percent-encoded: %q", next)
	}
	u, err := url.Parse(next)
	if err != nil {
		t.Fatalf("next unparseable: %q (%v)", next, err)
	}
	if got := u.Query().Get("cql"); got != cql {
		t.Fatalf("next cql = %q, want %q", got, cql)
	}

	// desc: newest first (the derived pages are stamped 2026-08-10, the
	// fixture page 2026-08-05, so page 20060 — minute 50 — leads).
	q = url.Values{}
	q.Set("cql", cql+" order by lastmodified desc")
	q.Set("limit", "5")
	code, v = originJSON(t, base, "/wiki/rest/api/content/search?"+q.Encode())
	if code != http.StatusOK {
		t.Fatalf("wiki search desc: %d %v", code, v)
	}
	results, _ := v["results"].([]any)
	if len(results) == 0 {
		t.Fatal("desc search returned no results")
	}
	first, _ := results[0].(map[string]any)
	if first["id"] != "20060" {
		t.Fatalf("desc first result = %v, want 20060 (newest)", first["id"])
	}

	// Real gadak across the same boundary: 52 pages mirrored means the
	// encoded next link was produced, followed and understood.
	bin := buildGadak(t)
	home := writeGadakHome(t, base)
	run := gadakRunner(t, bin, home)
	run("sync", "--full")

	if n := gadakDBPages(t, home); n != 52 {
		t.Fatalf("gadak mirrored %d wiki pages, want 52 — the next link broke somewhere", n)
	}
}

// gadakDBPages counts rows in the mirror's pages table.
func gadakDBPages(t *testing.T, home string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "gadak.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
