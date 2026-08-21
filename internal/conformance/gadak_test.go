package conformance

// Gadak conformance is the v0 acceptance test: point gadak at issuetap
// and sync. If this fails, the emulation is not real.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func gadakSrc(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ISSUETAP_GADAK_SRC"); p != "" {
		return p
	}
	candidates := []string{
		filepath.Join(fixtures.RepoRoot(), "..", "gadak"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "cmd", "gadak")); err == nil && st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	if os.Getenv("ISSUETAP_REQUIRE_GADAK") == "1" {
		t.Fatal("gadak source not found; set ISSUETAP_GADAK_SRC")
	}
	t.Skip("gadak source not found; set ISSUETAP_GADAK_SRC to run conformance")
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/conformance → repo root
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func buildGadak(t *testing.T) string {
	t.Helper()
	src := gadakSrc(t)
	out := filepath.Join(t.TempDir(), "gadak-it")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/gadak")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build gadak: %v\n%s", err, b)
	}
	return out
}

func startIssuetap(t *testing.T, fixture string, loc locale.Code, fs []faults.Fault) (base string, st *store.Store) {
	t.Helper()
	doc, err := fixtures.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	st = store.New(store.Options{Seed: 1, Locale: loc})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	s := api.New(cfg, st, faults.New(fs), nil, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String(), st
}

func writeGadakHome(t *testing.T, site string) string {
	t.Helper()
	home := t.TempDir()
	off := false
	cfg := map[string]any{
		"site":        site,
		"email":       "you@example.com",
		"token":       "issuetap",
		"updateCheck": off,
		"notify":      off,
		"confluence":  map[string]any{"spaces": []string{"DOCS"}},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestGadakSyncMirror(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(),
		"GADAK_HOME="+home,
		"GADAK_PROFILE=",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync:\n%s", out)
	if err != nil {
		t.Fatalf("gadak sync: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(home, "gadak.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var issues, comments, hist, pages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM changelog`).Scan(&hist); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if issues != 3 {
		t.Errorf("issues=%d want 3", issues)
	}
	if comments < 2 {
		t.Errorf("comments=%d want >=2", comments)
	}
	if hist < 3 {
		t.Errorf("changelog=%d want >=3", hist)
	}
	if pages != 1 {
		t.Errorf("pages=%d want 1", pages)
	}

	var keys string
	rows, err := db.Query(`SELECT key FROM issues ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		got = append(got, k)
	}
	keys = strings.Join(got, ",")
	if keys != "TAP-1,TAP-2,TAP-3" {
		t.Errorf("keys=%s", keys)
	}

	var status, statusID string
	if err := db.QueryRow(`SELECT status, status_id FROM issues WHERE key='TAP-1'`).Scan(&status, &statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != "3" {
		t.Errorf("TAP-1 status_id=%s want 3", statusID)
	}
	if status == "" {
		t.Error("TAP-1 status name empty")
	}
}

func TestGadakStopsOnRevokedCredential(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, []faults.Fault{{
		Name: "revoke-mid-sync", After: 2, Status: 401, PathPrefix: "/rest/",
	}})
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync (revoked):\n%s", out)
	if err == nil {
		t.Fatal("expected gadak sync to fail after 401")
	}
	if !strings.Contains(string(out), "credential rejected") && !strings.Contains(err.Error(), "exit") {
		// gadak prints the error; accept any non-zero.
		t.Logf("err=%v", err)
	}
}

func TestGadakBackoffOn429(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, []faults.Fault{{
		Name: "one-429", Times: 1, Status: 429, RetryAfter: 1, PathContains: "/search/jql",
	}})
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync (429):\n%s", out)
	if err != nil {
		t.Fatalf("gadak should succeed after retrying 429: %v", err)
	}
}

// TestGadakMirrorsPageVersionHistory is the seam between two independently
// built halves: issuetap serves GET /wiki/rest/api/content/{id}/version
// newest-first, and gadak's collector writes page_versions. Neither side's
// own tests can prove they agree — issuetap's assert its JSON, gadak's assert
// against a local httptest fake. Only real gadak over real HTTP does.
//
// It also pins the two properties most likely to rot silently across the
// seam: the editor's message must survive (it is the only field carrying
// human intent, and the reason the table exists), and gadak must not inherit
// the server's ordering (issuetap answers descending; the mirror stores and
// reads ascending).
func TestGadakMirrorsPageVersionHistory(t *testing.T) {
	src := gadakSrc(t)
	// CI checks out gadak's main, a moving target, and this capability had to
	// ship here before gadak could consume it — so at the moment issuetap
	// lands, gadak's main necessarily predates it. Skipping on "this gadak has
	// no page_versions migration at all" is what makes that ordering legal
	// without giving up the gate: a current gadak that stopped collecting
	// still has the table, so it runs and fails.
	schema, err := os.ReadFile(filepath.Join(src, "internal", "store", "schema.go"))
	if err != nil {
		t.Fatalf("read gadak schema: %v", err)
	}
	if !bytes.Contains(schema, []byte("page_versions")) {
		t.Skipf("gadak at %s predates page_versions; nothing to conform to yet", src)
	}

	bin := buildGadak(t)
	root := repoRoot(t)

	// A dedicated fixture: tiny.yaml is shared by six test files, and this
	// needs a multi-version page. Derive it rather than editing the original.
	fixtureSrc, err := os.ReadFile(filepath.Join(root, "examples/fixtures/tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "    space: DOCS\n    version: 1\n"
	if !strings.Contains(string(fixtureSrc), anchor) {
		t.Fatalf("tiny.yaml page anchor not found; update this test to match the fixture:\n%q", anchor)
	}
	withVersions := strings.Replace(string(fixtureSrc), anchor, "    space: DOCS\n"+
		"    version: 3\n"+
		"    versions:\n"+
		"      - number: 1\n"+
		"        when: \"2026-08-01T00:00:00.000Z\"\n"+
		"        author: 5b10a2844c20165700ede21g\n"+
		"        message: initial draft\n"+
		"      - number: 2\n"+
		"        when: \"2026-08-02T00:00:00.000Z\"\n"+
		"        author: 5b10a2844c20165700ede22g\n"+
		"        message: tightened the retention paragraph\n"+
		"      - number: 3\n"+
		"        when: \"2026-08-03T00:00:00.000Z\"\n"+
		"        author: 5b10a2844c20165700ede21g\n"+
		"        message: \"\"\n", 1)
	fixture := filepath.Join(t.TempDir(), "page-versions.yaml")
	if err := os.WriteFile(fixture, []byte(withVersions), 0o600); err != nil {
		t.Fatal(err)
	}

	base, _ := startIssuetap(t, fixture, locale.EN, nil)
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home, "GADAK_PROFILE=")
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync:\n%s", out)
	if err != nil {
		t.Fatalf("gadak sync: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(home, "gadak.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT pv.number, pv.message, pv.author_name
		FROM page_versions pv
		JOIN items i ON i.id = pv.item_id
		WHERE i.external_id = '20001'
		ORDER BY pv.number ASC`)
	if err != nil {
		t.Fatalf("page_versions query: %v", err)
	}
	defer rows.Close()

	type stamp struct {
		number  int
		message string
		author  string
	}
	var got []stamp
	for rows.Next() {
		var s stamp
		if err := rows.Scan(&s.number, &s.message, &s.author); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("page_versions rows=%d want 3: %+v", len(got), got)
	}
	// Ascending in the mirror even though the server answers descending.
	for i, want := range []int{1, 2, 3} {
		if got[i].number != want {
			t.Errorf("row %d number=%d want %d", i, got[i].number, want)
		}
	}
	if got[0].message != "initial draft" {
		t.Errorf("v1 message=%q want %q", got[0].message, "initial draft")
	}
	if got[1].message != "tightened the retention paragraph" {
		t.Errorf("v2 message=%q want %q", got[1].message, "tightened the retention paragraph")
	}
	if got[2].message != "" {
		t.Errorf("v3 message=%q want empty", got[2].message)
	}
	// The author is resolved to a display name, not left as an accountId.
	if got[1].author != "Dana" {
		t.Errorf("v2 author_name=%q want %q", got[1].author, "Dana")
	}

	// No bodies. The table exists to stay cheap; a body column would mean the
	// mirror grows with edits-per-page.
	var cols int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('page_versions')
		WHERE name IN ('body','body_adf','body_text')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Errorf("page_versions has %d body column(s); stamps only", cols)
	}
}

// TestGadakCreateFieldsRequiredSet is the seam for GET
// /issue/createmeta/{project}/issuetypes/{id}: gadak's CreateFields client
// (internal/jira, already on gadak main) must actually receive the required
// set from a live issuetap. Gadak's own tests hit an httptest fake; this is
// the only proof the two sides agree.
func TestGadakCreateFieldsRequiredSet(t *testing.T) {
	src := gadakSrc(t)
	origPath := filepath.Join(src, "internal", "jira", "create_fields_test.go")
	orig, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read gadak create_fields_test.go: %v — gadak predates CreateFields?", err)
	}

	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)

	patched := string(orig)
	if !strings.Contains(patched, "\t\"os\"\n") {
		patched = strings.Replace(patched, "\t\"net/http\"\n", "\t\"net/http\"\n\t\"os\"\n", 1)
	}
	patched += `

func TestIssuetapCreateFieldsRequiredSet(t *testing.T) {
	site := os.Getenv("ISSUETAP_BASE")
	if site == "" {
		t.Fatal("ISSUETAP_BASE unset")
	}
	c := New(site, "you@example.com", "issuetap")
	c.Retries = 1
	c.Backoff = 0
	got, err := c.CreateFields(context.Background(), "TAP", "10003")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]CreateFieldMeta{}
	for _, f := range got {
		byID[f.FieldID] = f
	}
	for _, id := range []string{"project", "summary", "issuetype", "reporter"} {
		f, ok := byID[id]
		if !ok {
			t.Errorf("missing %s in %+v", id, got)
			continue
		}
		if !f.Required {
			t.Errorf("%s required=false", id)
		}
	}
	if f := byID["reporter"]; !f.HasDefaultValue {
		t.Errorf("reporter hasDefaultValue=false")
	}
	if f := byID["issuetype"]; !f.HasDefaultValue {
		t.Errorf("issuetype hasDefaultValue=false")
	}
	if f := byID["summary"]; f.HasDefaultValue {
		t.Errorf("summary hasDefaultValue=true")
	}
}
`
	repl := filepath.Join(t.TempDir(), "create_fields_test.go")
	if err := os.WriteFile(repl, []byte(patched), 0o600); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{origPath: repl}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "test", "-overlay", overlayPath, "-count=1", "-timeout", "60s",
		"-run", "^TestIssuetapCreateFieldsRequiredSet$", ".")
	cmd.Dir = filepath.Join(src, "internal", "jira")
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"ISSUETAP_BASE="+base,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak CreateFields probe:\n%s", out)
	if err != nil {
		t.Fatalf("gadak CreateFields against issuetap: %v", err)
	}
}

// TestGadakLinkRoundTrip is the seam for GET /issueLinkType + POST /issueLink:
// gadak link resolves --type blocks against the catalog, POSTs type.id, then
// both issues must show the Cloud direction (outward sees outwardIssue,
// inward sees inwardIssue). Gadak's own tests hit an httptest fake.
func TestGadakLinkRoundTrip(t *testing.T) {
	src := gadakSrc(t)
	if _, err := os.Stat(filepath.Join(src, "cmd", "gadak", "link.go")); err != nil {
		t.Skipf("gadak at %s predates gadak link", src)
	}

	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)
	home := writeGadakHome(t, base)

	sync := exec.Command(bin, "sync", "--full")
	sync.Env = append(os.Environ(), "GADAK_HOME="+home, "GADAK_PROFILE=")
	out, err := sync.CombinedOutput()
	t.Logf("gadak sync:\n%s", out)
	if err != nil {
		t.Fatalf("gadak sync: %v", err)
	}

	cmd := exec.Command(bin, "link", "TAP-1", "TAP-3", "--type", "blocks")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home, "GADAK_PROFILE=")
	out, err = cmd.CombinedOutput()
	t.Logf("gadak link:\n%s", out)
	if err != nil {
		t.Fatalf("gadak link: %v\n%s", err, out)
	}

	a := issueLinksJSON(t, base, "TAP-1")
	b := issueLinksJSON(t, base, "TAP-3")
	if !hasJSONDirectedLink(a, "Blocks", "outwardIssue", "TAP-3") {
		t.Fatalf("TAP-1 missing outwardIssue TAP-3: %v", a)
	}
	if hasJSONDirectedLink(a, "Blocks", "inwardIssue", "TAP-3") {
		t.Fatalf("TAP-1 should not list TAP-3 as inwardIssue: %v", a)
	}
	if !hasJSONDirectedLink(b, "Blocks", "inwardIssue", "TAP-1") {
		t.Fatalf("TAP-3 missing inwardIssue TAP-1: %v", b)
	}
	if hasJSONDirectedLink(b, "Blocks", "outwardIssue", "TAP-1") {
		t.Fatalf("TAP-3 should not list TAP-1 as outwardIssue: %v", b)
	}
}

func issueLinksJSON(t *testing.T, base, key string) []any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/rest/api/3/issue/"+key+"?fields=issuelinks", nil)
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
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status %d body %v", key, res.StatusCode, v)
	}
	fields, _ := v["fields"].(map[string]any)
	raw, _ := fields["issuelinks"].([]any)
	return raw
}

func hasJSONDirectedLink(links []any, typeName, side, otherKey string) bool {
	for _, item := range links {
		l, ok := item.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := l["type"].(map[string]any)
		if typ == nil || typ["name"] != typeName {
			continue
		}
		other, _ := l[side].(map[string]any)
		if other != nil && other["key"] == otherKey {
			return true
		}
	}
	return false
}
