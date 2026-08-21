package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// GDK-505: origin rejects a parent that is not exactly one hierarchyLevel
// above the child. Cloud create and edit use different errors-map keys;
// a shared-key implementation must fail these tests.

func TestPostIssueRejectsSameLevelParent(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// TAP-2 is type 10003 (Task, level 0). A Task under that Task is illegal.
	status, body := postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "task under task",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-2"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("POST Task under Task status %d, want 400 body=%v", status, body)
	}
	assertCreateParentKeys(t, body)

	// Epic under Epic is the same violation at level 1.
	epic := mustCreateIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic a",
		"issuetype": map[string]any{"id": "10000"},
	})
	status, body = postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic under epic",
		"issuetype": map[string]any{"id": "10000"},
		"parent":    map[string]any{"key": epic},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("POST Epic under Epic status %d, want 400 body=%v", status, body)
	}
	assertCreateParentKeys(t, body)
}

func TestPostIssueAcceptsLegalHierarchy(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	epic := mustCreateIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "epic parent",
		"issuetype": map[string]any{"id": "10000"},
	})
	status, body := postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "task under epic",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": epic},
	})
	if status != http.StatusCreated {
		t.Fatalf("POST Task under Epic status %d, want 201 body=%v", status, body)
	}

	status, body = postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "sub-task under task",
		"issuetype": map[string]any{"id": "10002"},
		"parent":    map[string]any{"key": "TAP-2"},
	})
	if status != http.StatusCreated {
		t.Fatalf("POST Sub-task under Task status %d, want 201 body=%v", status, body)
	}
}

func TestPutIssueRejectsSameLevelParentWithPidKey(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// TAP-2 and TAP-3 are both Task (level 0). create-only rejection
	// would leave this PUT as a bypass.
	status, body := putIssue(t, ts, "TAP-2", map[string]any{
		"parent": map[string]any{"key": "TAP-3"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("PUT Task parent=Task status %d, want 400 body=%v", status, body)
	}
	assertEditParentKeys(t, body)
}

func TestPostIssueRejectsUnknownParent(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, body := postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "orphan parent",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-MISSING"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("POST unknown parent status %d, want 400 body=%v", status, body)
	}
	assertCreateParentKeys(t, body)
}

func TestPutIssueRejectsUnknownParentWithPidKey(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, body := putIssue(t, ts, "TAP-2", map[string]any{
		"parent": map[string]any{"key": "TAP-MISSING"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("PUT unknown parent status %d, want 400 body=%v", status, body)
	}
	assertEditParentKeys(t, body)
}

func TestPostIssueParentHierarchyUsesTypeIDNotName(t *testing.T) {
	// korean.yaml names type 10003 작업, not Task. A name-keyed
	// implementation silently accepts Task-under-Task here.
	ts := testServerNamed(t, "korean.yaml", locale.KO)
	defer ts.Close()

	status, body := postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "작업 under 작업",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": "TAP-2"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("korean POST same-level status %d, want 400 body=%v", status, body)
	}
	assertCreateParentKeys(t, body)

	epic := mustCreateIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "에픽",
		"issuetype": map[string]any{"id": "10000"},
	})
	status, body = postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "작업 under 에픽",
		"issuetype": map[string]any{"id": "10003"},
		"parent":    map[string]any{"key": epic},
	})
	if status != http.StatusCreated {
		t.Fatalf("korean POST legal hierarchy status %d, want 201 body=%v", status, body)
	}
}

func TestPostIssueOmittingParentStillSucceeds(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	status, body := postIssue(t, ts, map[string]any{
		"project":   map[string]any{"key": "TAP"},
		"summary":   "no parent",
		"issuetype": map[string]any{"id": "10003"},
	})
	if status != http.StatusCreated {
		t.Fatalf("POST without parent status %d, want 201 body=%v", status, body)
	}
}

func testServerNamed(t *testing.T, name string, loc locale.Code) *httptest.Server {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example(name))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(store.Options{Seed: 1, Locale: loc})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	s := api.New(cfg, st, nil, nil, false)
	return httptest.NewServer(s.Handler())
}

func postIssue(t *testing.T, ts *httptest.Server, fields map[string]any) (int, map[string]any) {
	t.Helper()
	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{"fields": fields})
	return decodeStatus(t, res)
}

func putIssue(t *testing.T, ts *httptest.Server, key string, fields map[string]any) (int, map[string]any) {
	t.Helper()
	res := authPut(t, ts, "/rest/api/3/issue/"+key, map[string]any{"fields": fields})
	return decodeStatus(t, res)
}

func mustCreateIssue(t *testing.T, ts *httptest.Server, fields map[string]any) string {
	t.Helper()
	status, body := postIssue(t, ts, fields)
	if status != http.StatusCreated {
		t.Fatalf("create status %d body=%v", status, body)
	}
	key, _ := body["key"].(string)
	if key == "" {
		t.Fatalf("create succeeded without key: %v", body)
	}
	return key
}

func decodeStatus(t *testing.T, res *http.Response) (int, map[string]any) {
	t.Helper()
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		return res.StatusCode, map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("json %v body=%s", err, raw)
	}
	return res.StatusCode, v
}

func errorMap(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	errs, _ := body["errors"].(map[string]any)
	if errs == nil {
		t.Fatalf("missing errors map: %v", body)
	}
	return errs
}

func assertCreateParentKeys(t *testing.T, body map[string]any) {
	t.Helper()
	errs := errorMap(t, body)
	if errs["parent"] == nil || errs["parentId"] == nil {
		t.Fatalf("create must set errors.parent and errors.parentId, got %v", body)
	}
	if errs["pid"] != nil {
		t.Fatalf("create must not use errors.pid (that is the edit key), got %v", body)
	}
}

func assertEditParentKeys(t *testing.T, body map[string]any) {
	t.Helper()
	errs := errorMap(t, body)
	if errs["pid"] == nil {
		t.Fatalf("edit must set errors.pid, got %v", body)
	}
	if errs["parent"] != nil || errs["parentId"] != nil {
		t.Fatalf("edit must not use create keys parent/parentId, got %v", body)
	}
}
