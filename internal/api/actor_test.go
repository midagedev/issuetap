package api_test

// X-Issuetap-Actor contract (gadak GDK-588): several coding agents share
// one standalone workspace, so writes must attribute to the agent that
// made them, not the single Basic-auth account. The header carries a
// stable opaque slug used as the accountId verbatim; an unknown slug is
// provisioned as an accountType "agent" user. The Basic-auth
// X-Issuetap-User channel keeps its old behavior, and authorize() must
// not strip the actor header on the Basic path.
//
// | Contract | Test |
// | --- | --- |
// | actor header wins over the Basic identity | TestActorHeaderStampsCommentAuthor |
// | X-Issuetap-Actor-Name is the display name; without it a deterministic alias (GDK-593) | TestActorNameHeaderIsDisplayName |
// | a repeated slug is one stable user (no rename, no duplicate) | TestActorSlugIsStableAcrossRequests |
// | GET /myself and /user/search?query=me answer the agent | TestActorMyselfAndUserSearchMe |
// | blank header is ignored; Basic fallback intact | TestActorBlankHeaderFallsBackToBasicIdentity |
// | slug longer than 128 chars is 400; 128 is not | TestActorHeaderTooLongIs400 |
// | create reporter fallback is the request actor | TestCreateReporterFallsBackToRequestActor |
// | assignee/transition changelog author is the request actor | TestActorStampsChangelogAuthors |
// | agent authors render accountType "agent" | TestAgentAccountTypeRendersOnAuthors |
// | accountType "agent" survives a persist reload | TestAgentAccountTypeSurvivesPersistReload |

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// actorGet/actorPost/actorPut are authGet/authPost with the actor headers.
// An empty actor sends no header (the plain Basic path).
func actorGet(t *testing.T, ts *httptest.Server, actor, actorName, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Accept", "application/json")
	if actor != "" {
		req.Header.Set("X-Issuetap-Actor", actor)
	}
	if actorName != "" {
		req.Header.Set("X-Issuetap-Actor-Name", actorName)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func actorPost(t *testing.T, ts *httptest.Server, actor, actorName, path string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, r)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor != "" {
		req.Header.Set("X-Issuetap-Actor", actor)
	}
	if actorName != "" {
		req.Header.Set("X-Issuetap-Actor-Name", actorName)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func actorPut(t *testing.T, ts *httptest.Server, actor, actorName, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, ts.URL+path, strings.NewReader(body))
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	if actor != "" {
		req.Header.Set("X-Issuetap-Actor", actor)
	}
	if actorName != "" {
		req.Header.Set("X-Issuetap-Actor-Name", actorName)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func commentBody(text string) map[string]any {
	return map[string]any{
		"body": map[string]any{"type": "doc", "version": 1, "content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": text},
			}},
		}},
	}
}

// TestActorHeaderStampsCommentAuthor: with Basic auth as Ada, a POST
// comment carrying X-Issuetap-Actor is authored by the agent — proving
// both the precedence (actor over Basic) and that authorize()'s Basic
// path does not strip the header.
func TestActorHeaderStampsCommentAuthor(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := actorPost(t, ts, "claude:354bff2b", "Claude (build 1)", "/rest/api/3/issue/TAP-2/comment", commentBody("agent note"))
	if res.StatusCode != 201 {
		t.Fatalf("comment status %d", res.StatusCode)
	}
	v := decode(t, res)
	author := v["author"].(map[string]any)
	if author["accountId"] != "claude:354bff2b" {
		t.Fatalf("author accountId=%v, want the actor slug claude:354bff2b (not the Basic user)", author["accountId"])
	}
	if author["displayName"] != "Claude (build 1)" {
		t.Fatalf("author displayName=%v, want X-Issuetap-Actor-Name", author["displayName"])
	}
	// Read-back: the stored comment keeps the agent author.
	list := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-2/comment"))
	cms := list["comments"].([]any)
	last := cms[len(cms)-1].(map[string]any)
	if last["author"].(map[string]any)["accountId"] != "claude:354bff2b" {
		t.Fatalf("stored comment author=%v, want the agent", last["author"])
	}
}

// TestActorNameHeaderIsDisplayName: X-Issuetap-Actor-Name is the display
// name. Without it the slug is no longer shown raw — a nameless slug gets
// the deterministic alias (GDK-593): "Adj Noun" plus the harness label
// when the prefix is known, stable across requests.
func TestActorNameHeaderIsDisplayName(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, actorGet(t, ts, "grok:tars", "", "/rest/api/3/myself"))
	if v["accountId"] != "grok:tars" {
		t.Fatalf("accountId=%v, want the slug", v["accountId"])
	}
	name, _ := v["displayName"].(string)
	alias := regexp.MustCompile(`^[A-Z][a-z]+ [A-Z][a-z]+( \d+)? \(Grok\)$`)
	if !alias.MatchString(name) {
		t.Fatalf("displayName=%q, want a generated \"Adj Noun (Grok)\" alias when no actor name is sent", name)
	}
	again := decode(t, actorGet(t, ts, "grok:tars", "", "/rest/api/3/myself"))
	if again["displayName"] != name {
		t.Fatalf("repeat request: displayName=%v, want the same alias %q", again["displayName"], name)
	}
}

// TestActorSlugIsStableAcrossRequests: the second request with the same
// slug (and no name header) must return the already-provisioned user —
// same accountId, display name not rewritten, one user.
func TestActorSlugIsStableAcrossRequests(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	first := decode(t, actorGet(t, ts, "hermes:tars", "Hermes", "/rest/api/3/myself"))
	if first["accountId"] != "hermes:tars" || first["displayName"] != "Hermes" {
		t.Fatalf("first myself=%v", first)
	}
	second := decode(t, actorGet(t, ts, "hermes:tars", "", "/rest/api/3/myself"))
	if second["accountId"] != "hermes:tars" {
		t.Fatalf("second myself accountId=%v, want the same slug", second["accountId"])
	}
	if second["displayName"] != "Hermes" {
		t.Fatalf("repeat request renamed the agent: displayName=%v, want Hermes", second["displayName"])
	}
	arr := decodeArr(t, actorGet(t, ts, "", "", "/rest/api/3/user/search?query=Hermes"))
	if len(arr) != 1 || arr[0].(map[string]any)["accountId"] != "hermes:tars" {
		t.Fatalf("user/search Hermes=%v, want exactly the one agent", arr)
	}
}

// TestActorMyselfAndUserSearchMe: both current-user surfaces answer the
// agent identity for an actor-headed request.
func TestActorMyselfAndUserSearchMe(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	myself := decode(t, actorGet(t, ts, "claude:354bff2b", "", "/rest/api/3/myself"))
	if myself["accountId"] != "claude:354bff2b" {
		t.Fatalf("myself accountId=%v, want the agent", myself["accountId"])
	}
	arr := decodeArr(t, actorGet(t, ts, "claude:354bff2b", "", "/rest/api/3/user/search?query=me"))
	if len(arr) != 1 {
		t.Fatalf("query=me returned %d users, want 1 (the agent)", len(arr))
	}
	if arr[0].(map[string]any)["accountId"] != myself["accountId"] {
		t.Fatalf("query=me accountId=%v, myself=%v", arr[0], myself["accountId"])
	}
}

// TestActorBlankHeaderFallsBackToBasicIdentity: a whitespace-only header
// is ignored — identity falls through to the Basic username.
func TestActorBlankHeaderFallsBackToBasicIdentity(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	for _, header := range []string{"", "   "} {
		v := decode(t, actorGet(t, ts, header, "", "/rest/api/3/myself"))
		if v["accountId"] != "5b10a2844c20165700ede21g" {
			t.Fatalf("actor header %q: myself accountId=%v, want the Basic user Ada", header, v["accountId"])
		}
	}
}

// TestActorHeaderTooLongIs400: the slug is capped at 128 characters.
func TestActorHeaderTooLongIs400(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := actorGet(t, ts, strings.Repeat("a", 129), "", "/rest/api/3/myself")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("129-char slug status %d, want 400", res.StatusCode)
	}
	res.Body.Close()
	// Boundary: 128 is legal.
	v := decode(t, actorGet(t, ts, strings.Repeat("a", 128), "", "/rest/api/3/myself"))
	if v["accountId"] != strings.Repeat("a", 128) {
		t.Fatalf("128-char slug accountId=%v", v["accountId"])
	}
}

// TestCreateReporterFallsBackToRequestActor: a create without
// fields.reporter reports as the request actor; without the actor header
// the Basic user (Ada) is the reporter — the fallback must not become a
// random pick now that the workspace has more than one user.
func TestCreateReporterFallsBackToRequestActor(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	fields := map[string]any{"project": map[string]any{"key": "TAP"}, "summary": "filed by an agent"}
	created := decode(t, actorPost(t, ts, "claude:354bff2b", "", "/rest/api/3/issue", map[string]any{"fields": fields}))
	key, _ := created["key"].(string)
	if key == "" {
		t.Fatalf("create response=%v", created)
	}
	v := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/"+key+"?fields=reporter,creator"))
	flds := v["fields"].(map[string]any)
	reporter := flds["reporter"].(map[string]any)
	if reporter["accountId"] != "claude:354bff2b" {
		t.Fatalf("reporter=%v, want the request actor", reporter)
	}
	if creator := flds["creator"].(map[string]any); creator["accountId"] != "claude:354bff2b" {
		t.Fatalf("creator=%v, want the request actor", creator)
	}

	// Control: no actor header → the Basic user is the reporter.
	created2 := decode(t, actorPost(t, ts, "", "", "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{"project": map[string]any{"key": "TAP"}, "summary": "filed by the human"},
	}))
	key2, _ := created2["key"].(string)
	v2 := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/"+key2+"?fields=reporter"))
	rep2 := v2["fields"].(map[string]any)["reporter"].(map[string]any)
	if rep2["accountId"] != "5b10a2844c20165700ede21g" {
		t.Fatalf("no-actor reporter=%v, want the Basic user Ada", rep2)
	}
}

// TestActorStampsChangelogAuthors: assignee and transition changes made
// by an actor-headed request record that agent as the changelog author.
// TAP-2 has no authored history, so every row here is written by the test.
func TestActorStampsChangelogAuthors(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	actor := "grok:uuid-1"
	res := actorPut(t, ts, actor, "", "/rest/api/3/issue/TAP-2/assignee", `{"accountId":"5b10a2844c20165700ede22g"}`)
	if res.StatusCode != 204 {
		t.Fatalf("assignee status %d", res.StatusCode)
	}
	res.Body.Close()

	tr := decode(t, actorGet(t, ts, actor, "", "/rest/api/3/issue/TAP-2/transitions"))
	trs := tr["transitions"].([]any)
	if len(trs) == 0 {
		t.Fatal("no transitions")
	}
	id, _ := trs[0].(map[string]any)["id"].(string)
	res = actorPost(t, ts, actor, "", "/rest/api/3/issue/TAP-2/transitions", map[string]any{
		"transition": map[string]any{"id": id},
	})
	if res.StatusCode != 204 {
		t.Fatalf("transition status %d", res.StatusCode)
	}
	res.Body.Close()

	cl := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-2/changelog?maxResults=100"))
	vals := cl["values"].([]any)
	if len(vals) != 2 {
		t.Fatalf("changelog rows=%d, want the assignee + transition rows", len(vals))
	}
	for _, raw := range vals {
		row := raw.(map[string]any)
		if got := row["author"].(map[string]any)["accountId"]; got != actor {
			t.Fatalf("changelog row author=%v, want the request actor %s", got, actor)
		}
	}
}

// TestAgentAccountTypeRendersOnAuthors: agent-authored records carry
// accountType "agent" on every user surface (myself, comment author,
// changelog author).
func TestAgentAccountTypeRendersOnAuthors(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	actor := "claude:354bff2b"
	myself := decode(t, actorGet(t, ts, actor, "", "/rest/api/3/myself"))
	if myself["accountType"] != "agent" {
		t.Fatalf("myself accountType=%v, want agent", myself["accountType"])
	}
	v := decode(t, actorPost(t, ts, actor, "", "/rest/api/3/issue/TAP-2/comment", commentBody("typed")))
	if v["author"].(map[string]any)["accountType"] != "agent" {
		t.Fatalf("comment author accountType=%v, want agent", v["author"])
	}
	res := actorPut(t, ts, actor, "", "/rest/api/3/issue/TAP-2/assignee", `{"accountId":"5b10a2844c20165700ede22g"}`)
	res.Body.Close()
	cl := decode(t, actorGet(t, ts, "", "", "/rest/api/3/issue/TAP-2/changelog"))
	vals := cl["values"].([]any)
	last := vals[len(vals)-1].(map[string]any)
	if last["author"].(map[string]any)["accountType"] != "agent" {
		t.Fatalf("changelog author accountType=%v, want agent", last["author"])
	}
}

// TestAgentAccountTypeSurvivesPersistReload: an agent user provisioned
// through the actor header keeps accountType "agent" across a persist
// write + reload (durable mode, so the file is current before Close).
func TestAgentAccountTypeSurvivesPersistReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	// openPersisted starts a server on the persistence file. seed=true
	// applies the fixture (first run); the reload run seeds nothing —
	// re-applying would overwrite TAP-2 and erase the persisted comment.
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
	v := decode(t, actorPost(t, ts1, "claude:354bff2b", "", "/rest/api/3/issue/TAP-2/comment", commentBody("survive")))
	if v["author"].(map[string]any)["accountId"] != "claude:354bff2b" {
		t.Fatalf("pre-reload author=%v", v["author"])
	}

	ts2 := openPersisted(false) // re-opens the persisted file
	list := decode(t, actorGet(t, ts2, "", "", "/rest/api/3/issue/TAP-2/comment"))
	cms := list["comments"].([]any)
	last := cms[len(cms)-1].(map[string]any)
	author := last["author"].(map[string]any)
	if author["accountId"] != "claude:354bff2b" {
		t.Fatalf("post-reload author=%v, want the agent slug", author)
	}
	if author["accountType"] != "agent" {
		t.Fatalf("post-reload accountType=%v, want agent (type was lost on the persist round trip)", author["accountType"])
	}
}
