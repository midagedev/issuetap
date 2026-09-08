package api_test

// Agile 1.0 contract tests (gadak GDK-1666, docs/decisions/0002). Every
// route happy path plus one violation each, the sprint state machine over
// every from→to pair, the close sweep, the Sprint field catalog row and
// its exclusion from editmeta/createmeta, the customfield_10020 value
// order, and the JQL sprint operators and functions. The tiny fixture has
// exactly one project (TAP, three issues): TAP-1 in progress, TAP-2 to do,
// TAP-3 done — the sweep needs one of each.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

const (
	agileStartDate = "2026-09-09T09:00:00.000+0900"
	agileEndDate   = "2026-09-23T18:00:00.000+0900"
)

func agileSprintDates() map[string]any {
	return map[string]any{"startDate": agileStartDate, "endDate": agileEndDate}
}

func createSprint(t *testing.T, ts *httptest.Server, name string, extra map[string]any) float64 {
	t.Helper()
	// Boards materialize lazily on first list — exactly what gadak does
	// before its first sprint create. Touch the list so board 1 exists.
	if boards := authGet(t, ts, "/rest/agile/1.0/board"); boards.StatusCode != http.StatusOK {
		boards.Body.Close()
		t.Fatalf("board list before create: status %d", boards.StatusCode)
	} else {
		boards.Body.Close()
	}
	body := map[string]any{"name": name, "originBoardId": 1}
	for k, v := range extra {
		body[k] = v
	}
	res := authPost(t, ts, "/rest/agile/1.0/sprint", body)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create sprint %q: status %d", name, res.StatusCode)
	}
	return decode(t, res)["id"].(float64)
}

func sprintURL(id float64, suffix string) string {
	return fmt.Sprintf("/rest/agile/1.0/sprint/%d%s", int64(id), suffix)
}

func moveToSprint(t *testing.T, ts *httptest.Server, id float64, keys ...string) *http.Response {
	t.Helper()
	return authPost(t, ts, sprintURL(id, "/issue"), map[string]any{"issues": keys})
}

func moveToBacklog(t *testing.T, ts *httptest.Server, keys ...string) *http.Response {
	t.Helper()
	return authPost(t, ts, "/rest/agile/1.0/backlog/issue", map[string]any{"issues": keys})
}

func setOf(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func searchKeys(t *testing.T, ts *httptest.Server, jql string) []string {
	t.Helper()
	res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{"jql": jql, "fields": []string{"key"}})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("jql %q: status %d", jql, res.StatusCode)
	}
	v := decode(t, res)
	var keys []string
	for _, raw := range v["issues"].([]any) {
		keys = append(keys, raw.(map[string]any)["key"].(string))
	}
	return keys
}

func keySet(t *testing.T, keys []string) map[string]bool {
	t.Helper()
	m := map[string]bool{}
	for _, k := range keys {
		if m[k] {
			t.Fatalf("duplicate key %s", k)
		}
		m[k] = true
	}
	return m
}

// sprintField returns the issue's customfield_10020 (nil or array).
func sprintField(t *testing.T, ts *httptest.Server, key string) any {
	t.Helper()
	res := authGet(t, ts, "/rest/api/3/issue/"+key+"?fields=customfield_10020")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue %s: status %d", key, res.StatusCode)
	}
	return decode(t, res)["fields"].(map[string]any)["customfield_10020"]
}

// sprintChangelogItems collects the Sprint changelog items (field "Sprint").
func sprintChangelogItems(t *testing.T, ts *httptest.Server, key string) []map[string]any {
	t.Helper()
	res := authGet(t, ts, "/rest/api/3/issue/"+key+"?expand=changelog")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("issue %s changelog: status %d", key, res.StatusCode)
	}
	cl := decode(t, res)["changelog"].(map[string]any)
	var out []map[string]any
	for _, h := range cl["histories"].([]any) {
		for _, it := range h.(map[string]any)["items"].([]any) {
			m := it.(map[string]any)
			if m["field"] == "Sprint" {
				out = append(out, m)
			}
		}
	}
	return out
}

func TestAgileBoardListShapeAndFilters(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	res := authGet(t, ts, "/rest/agile/1.0/board")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	v := decode(t, res)
	if v["total"] != float64(1) || v["isLast"] != true || v["startAt"] != float64(0) || v["maxResults"] != float64(50) {
		t.Fatalf("envelope=%v", v)
	}
	b := v["values"].([]any)[0].(map[string]any)
	if b["id"] != float64(1) || b["name"] != "TAP board" || b["type"] != "scrum" {
		t.Fatalf("board=%v", b)
	}
	loc := b["location"].(map[string]any)
	if loc["projectKey"] != "TAP" || loc["projectId"] != float64(10000) || loc["name"] != "Issuetap" {
		t.Fatalf("location=%v", loc)
	}

	// Filters: by key, by id, by type; a miss is an empty list, not 404.
	for path, wantN := range map[string]int{
		"?projectKeyOrId=TAP":            1,
		"?projectKeyOrId=10000":          1,
		"?projectKeyOrId=NOPE":           0,
		"?type=scrum":                    1,
		"?type=kanban":                   0,
		"?projectKeyOrId=TAP&type=scrum": 1,
	} {
		v := decode(t, authGet(t, ts, "/rest/agile/1.0/board"+path))
		if n := len(v["values"].([]any)); n != wantN {
			t.Fatalf("%s: %d boards, want %d", path, n, wantN)
		}
	}
}

func TestAgileBoardListPaging(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// One board: a one-page window is last; past the end is empty but last.
	v := decode(t, authGet(t, ts, "/rest/agile/1.0/board?maxResults=1"))
	if v["isLast"] != true || len(v["values"].([]any)) != 1 {
		t.Fatalf("page one=%v", v)
	}
	v = decode(t, authGet(t, ts, "/rest/agile/1.0/board?startAt=1"))
	if v["isLast"] != true || len(v["values"].([]any)) != 0 || v["total"] != float64(1) {
		t.Fatalf("past end=%v", v)
	}
}

func TestAgileBoardSprintsListAndStateFilter(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	id := createSprint(t, ts, "Sprint 1", agileSprintDates())

	v := decode(t, authGet(t, ts, "/rest/agile/1.0/board/1/sprint"))
	if v["total"] != float64(1) {
		t.Fatalf("sprints=%v", v)
	}
	sp := v["values"].([]any)[0].(map[string]any)
	if sp["id"] != id || sp["state"] != "future" || sp["name"] != "Sprint 1" {
		t.Fatalf("sprint=%v", sp)
	}
	if _, has := sp["startDate"]; !has {
		t.Fatal("startDate must be present when set")
	}
	if _, has := sp["completeDate"]; has {
		t.Fatal("completeDate must be omitted when unset")
	}

	// State filter: single and comma-separated; a miss is empty.
	for path, wantN := range map[string]int{
		"?state=future":        1,
		"?state=active":        0,
		"?state=active,future": 1,
	} {
		v := decode(t, authGet(t, ts, "/rest/agile/1.0/board/1/sprint"+path))
		if n := len(v["values"].([]any)); n != wantN {
			t.Fatalf("%s: %d sprints, want %d", path, n, wantN)
		}
	}

	// Violations: unknown board is the exact Agile 404; an unknown state
	// token is a 400, never a silent empty list.
	res := authGet(t, ts, "/rest/agile/1.0/board/999/sprint")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board status %d", res.StatusCode)
	}
	v = decode(t, res)
	if msg := v["errorMessages"].([]any)[0]; msg != "Board does not exist or you do not have permission to view it." {
		t.Fatalf("body=%v", msg)
	}
	if _, has := v["errors"]; !has {
		t.Fatal("errors key missing")
	}
	res = authGet(t, ts, "/rest/agile/1.0/board/1/sprint?state=bogus")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad state status %d", res.StatusCode)
	}
}

func TestAgileSprintCreate(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	if boards := authGet(t, ts, "/rest/agile/1.0/board"); boards.StatusCode != http.StatusOK {
		boards.Body.Close()
		t.Fatalf("board list: status %d", boards.StatusCode)
	} else {
		boards.Body.Close()
	}
	res := authPost(t, ts, "/rest/agile/1.0/sprint", map[string]any{
		"name": "Sprint A", "originBoardId": 1,
		"goal": "ship the docs", "startDate": agileStartDate, "endDate": agileEndDate,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", res.StatusCode)
	}
	sp := decode(t, res)
	if sp["state"] != "future" || sp["goal"] != "ship the docs" || sp["originBoardId"] != float64(1) {
		t.Fatalf("sprint=%v", sp)
	}

	// Ids mint from seq:sprint: the second sprint is 2, never a reuse.
	if id := createSprint(t, ts, "Sprint B", nil); id != 2 {
		t.Fatalf("second sprint id=%v", id)
	}

	// Violations: each missing key is a 400 naming the field.
	res = authPost(t, ts, "/rest/agile/1.0/sprint", map[string]any{"originBoardId": 1})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no name status %d", res.StatusCode)
	}
	if _, has := decode(t, res)["errors"].(map[string]any)["name"]; !has {
		t.Fatal("errors must name name")
	}
	res = authPost(t, ts, "/rest/agile/1.0/sprint", map[string]any{"name": "X"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("no board status %d", res.StatusCode)
	}
	if _, has := decode(t, res)["errors"].(map[string]any)["originBoardId"]; !has {
		t.Fatal("errors must name originBoardId")
	}
	res = authPost(t, ts, "/rest/agile/1.0/sprint", map[string]any{"name": "X", "originBoardId": 999})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown board status %d", res.StatusCode)
	}
}

// sprintInState builds a sprint already sitting in the wanted state, with
// or without dates, on a fresh id.
func sprintInState(t *testing.T, ts *httptest.Server, state string, withDates bool, n int) float64 {
	t.Helper()
	name := fmt.Sprintf("M%d", n)
	var extra map[string]any
	if withDates {
		extra = agileSprintDates()
	}
	id := createSprint(t, ts, name, extra)
	if state == "future" {
		return id
	}
	res := authPost(t, ts, sprintURL(id, ""), map[string]any(merge(agileSprintDates(), map[string]any{"state": "active"})))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("start helper: status %d", res.StatusCode)
	}
	if state == "active" {
		return id
	}
	res = authPost(t, ts, sprintURL(id, ""), map[string]any{"state": "closed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("close helper: status %d", res.StatusCode)
	}
	return id
}

func merge(ms ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestAgileSprintStateMachine(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	cases := []struct {
		name        string
		from, to    string
		createDates bool
		patchDates  bool
		wantStatus  int
		wantErr     string
	}{
		{"future to active", "future", "active", true, false, 200, ""},
		{"future to active with dates in the patch", "future", "active", false, true, 200, ""},
		{"future to active without any dates", "future", "active", false, false, 400, "startDate and endDate are required to start a sprint"},
		{"future to closed", "future", "closed", true, false, 400, "cannot transition sprint from future to closed"},
		{"active to closed", "active", "closed", true, false, 200, ""},
		{"active to future", "active", "future", true, false, 400, "cannot transition sprint from active to future"},
		{"closed to active", "closed", "active", true, false, 400, "cannot transition sprint from closed to active"},
		{"closed to future", "closed", "future", true, false, 400, "cannot transition sprint from closed to future"},
		{"same state is a no-op, not a transition", "active", "active", true, false, 200, ""},
	}
	for i, c := range cases {
		id := sprintInState(t, ts, c.from, c.createDates, i)
		patch := map[string]any{"state": c.to}
		if c.patchDates {
			mergeInto(patch, agileSprintDates())
		}
		res := authPost(t, ts, sprintURL(id, ""), patch)
		if res.StatusCode != c.wantStatus {
			t.Fatalf("%s: status %d, want %d", c.name, res.StatusCode, c.wantStatus)
		}
		if c.wantErr != "" {
			v := decode(t, res)
			msgs := v["errorMessages"].([]any)
			if len(msgs) == 0 || !contains(msgs[0].(string), c.wantErr) {
				t.Fatalf("%s: messages=%v, want %q", c.name, msgs, c.wantErr)
			}
			continue
		}
		sp := decode(t, res)
		if sp["state"] != c.to {
			t.Fatalf("%s: state=%v", c.name, sp["state"])
		}
	}

	// The legal transitions stamp their dates from the store clock.
	id := sprintInState(t, ts, "active", true, 90)
	sp := decode(t, authPost(t, ts, sprintURL(id, ""), map[string]any{"state": "closed"}))
	if sp["completeDate"] == nil || sp["completeDate"] == "" {
		t.Fatalf("close must stamp completeDate: %v", sp)
	}

	// PUT is the same partial update as POST.
	id = createSprint(t, ts, "PUT me", agileSprintDates())
	res := authPut(t, ts, sprintURL(id, ""), map[string]any{"goal": "renamed"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT status %d", res.StatusCode)
	}
	if sp := decode(t, res); sp["goal"] != "renamed" {
		t.Fatalf("PUT goal=%v", sp["goal"])
	}

	// Unknown sprint is the Agile 404; a bogus state value is a 400.
	res = authPost(t, ts, "/rest/agile/1.0/sprint/999", map[string]any{"state": "active"})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown sprint status %d", res.StatusCode)
	}
	if msg := decode(t, res)["errorMessages"].([]any)[0]; msg != "Sprint does not exist or you do not have permission to view it." {
		t.Fatalf("body=%v", msg)
	}
	res = authPost(t, ts, sprintURL(1, ""), map[string]any{"state": "bogus"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus state status %d", res.StatusCode)
	}
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestAgileCloseSweepsIncompleteKeepsDone is the spec's close test: TAP-1
// (in progress) moves to the backlog, TAP-3 (done) keeps the closed sprint,
// and both facts are visible in the issue JSON and the changelog.
func TestAgileCloseSweepsIncompleteKeepsDone(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	id := createSprint(t, ts, "Sprint 1", agileSprintDates())
	for _, res := range []*http.Response{
		moveToSprint(t, ts, id, "TAP-1"),
		moveToSprint(t, ts, id, "TAP-3"),
	} {
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("move status %d", res.StatusCode)
		}
	}
	if res := authPost(t, ts, sprintURL(id, ""), map[string]any(merge(agileSprintDates(), map[string]any{"state": "active"}))); res.StatusCode != http.StatusOK {
		t.Fatalf("start status %d", res.StatusCode)
	}

	sp := decode(t, authPost(t, ts, sprintURL(id, ""), map[string]any{"state": "closed"}))
	if sp["state"] != "closed" || sp["completeDate"] == nil {
		t.Fatalf("closed sprint=%v", sp)
	}

	// Incomplete → backlog: the whole list is cleared, value is null.
	if v := sprintField(t, ts, "TAP-1"); v != nil {
		t.Fatalf("TAP-1 after close: %v", v)
	}
	// Done → stays, listing the closed sprint.
	arr, ok := sprintField(t, ts, "TAP-3").([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("TAP-3 after close: %v", arr)
	}
	if m := arr[0].(map[string]any); m["state"] != "closed" || m["id"] != id {
		t.Fatalf("TAP-3 sprint=%v", m)
	}

	// Both moves recorded: one Sprint item in, one out for TAP-1; one in
	// (and none out) for TAP-3.
	items := sprintChangelogItems(t, ts, "TAP-1")
	if len(items) != 2 {
		t.Fatalf("TAP-1 sprint items=%d", len(items))
	}
	out := items[1]
	if out["to"] != "" || out["toString"] != "" || out["fromString"] != "Sprint 1" || out["fieldId"] != "customfield_10020" {
		t.Fatalf("sweep item=%v", out)
	}
	if items := sprintChangelogItems(t, ts, "TAP-3"); len(items) != 1 {
		t.Fatalf("TAP-3 sprint items=%d (done issues keep the sprint)", len(items))
	}
}

func TestAgileMoveIssues(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	id := createSprint(t, ts, "Sprint 1", agileSprintDates())

	// Happy: 204, membership visible on the issue.
	if res := moveToSprint(t, ts, id, "TAP-1"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	arr, ok := sprintField(t, ts, "TAP-1").([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("TAP-1 field=%v", arr)
	}

	// Moving again is a no-op: 204, and still exactly one Sprint item.
	if res := moveToSprint(t, ts, id, "TAP-1"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("re-move status %d", res.StatusCode)
	}
	if n := len(sprintChangelogItems(t, ts, "TAP-1")); n != 1 {
		t.Fatalf("re-move wrote %d items", n)
	}

	// Unknown key: 404 naming the key, and nothing half-applied — the
	// resolve happens before any write.
	res := moveToSprint(t, ts, id, "TAP-2", "NOPE-9")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown key status %d", res.StatusCode)
	}
	if _, has := decode(t, res)["errors"].(map[string]any)["NOPE-9"]; !has {
		t.Fatal("errors must name the unknown key")
	}
	if v := sprintField(t, ts, "TAP-2"); v != nil {
		t.Fatalf("TAP-2 must not be sprinted after a failed move: %v", v)
	}

	// Cap: 51 keys is a 400 before anything resolves.
	keys := make([]string, 51)
	for i := range keys {
		keys[i] = fmt.Sprintf("TAP-%d", i+1)
	}
	res = authPost(t, ts, sprintURL(id, "/issue"), map[string]any{"issues": keys})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("cap status %d", res.StatusCode)
	}

	// Empty body is a 400.
	res = authPost(t, ts, sprintURL(id, "/issue"), map[string]any{"issues": []string{}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty issues status %d", res.StatusCode)
	}

	// Closed sprint refuses new members.
	authPost(t, ts, sprintURL(id, ""), map[string]any(merge(agileSprintDates(), map[string]any{"state": "active"})))
	authPost(t, ts, sprintURL(id, ""), map[string]any{"state": "closed"})
	res = moveToSprint(t, ts, id, "TAP-2")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("closed sprint status %d", res.StatusCode)
	}
}

func TestAgileBacklogMove(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	id := createSprint(t, ts, "Sprint 1", agileSprintDates())
	moveToSprint(t, ts, id, "TAP-1")

	if res := moveToBacklog(t, ts, "TAP-1"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", res.StatusCode)
	}
	if v := sprintField(t, ts, "TAP-1"); v != nil {
		t.Fatalf("backlog must clear: %v", v)
	}
	if n := len(sprintChangelogItems(t, ts, "TAP-1")); n != 2 {
		t.Fatalf("items=%d (in then out)", n)
	}

	// An issue with no sprint is a no-op 204: no changelog, no error.
	if res := moveToBacklog(t, ts, "TAP-2"); res.StatusCode != http.StatusNoContent {
		t.Fatalf("no-op status %d", res.StatusCode)
	}
	if n := len(sprintChangelogItems(t, ts, "TAP-2")); n != 0 {
		t.Fatalf("no-op wrote %d items", n)
	}

	// Unknown key names the key.
	res := moveToBacklog(t, ts, "NOPE-9")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown key status %d", res.StatusCode)
	}
	if _, has := decode(t, res)["errors"].(map[string]any)["NOPE-9"]; !has {
		t.Fatal("errors must name the unknown key")
	}
}

func TestAgileSprintGetAndIssues(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	id := createSprint(t, ts, "Sprint 1", agileSprintDates())
	moveToSprint(t, ts, id, "TAP-1", "TAP-3")

	res := authGet(t, ts, sprintURL(id, ""))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get sprint status %d", res.StatusCode)
	}
	if sp := decode(t, res); sp["name"] != "Sprint 1" || sp["state"] != "future" {
		t.Fatalf("sprint=%v", sp)
	}

	v := decode(t, authGet(t, ts, sprintURL(id, "/issue")))
	got := map[string]bool{}
	for _, raw := range v["issues"].([]any) {
		got[raw.(map[string]any)["key"].(string)] = true
	}
	if len(got) != 2 || !got["TAP-1"] || !got["TAP-3"] {
		t.Fatalf("issues=%v", got)
	}

	res = authGet(t, ts, "/rest/agile/1.0/sprint/999")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown sprint status %d", res.StatusCode)
	}
	res = authGet(t, ts, "/rest/agile/1.0/sprint/999/issue")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown sprint issues status %d", res.StatusCode)
	}
}

// TestSprintFieldCatalog: the row exists with the gh-sprint schema (how
// gadak discovers it), and every write-shaped consumer excludes it —
// editmeta, createmeta, and PUT /issue, which must reject it rather than
// silently store a value the rendered array would shadow (GDK-1207 rule).
func TestSprintFieldCatalog(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	var row map[string]any
	for _, raw := range decodeArr(t, authGet(t, ts, "/rest/api/3/field")) {
		m := raw.(map[string]any)
		if m["id"] == "customfield_10020" {
			row = m
		}
	}
	if row == nil {
		t.Fatal("customfield_10020 missing from GET /field")
	}
	if row["name"] != "Sprint" || row["custom"] != true {
		t.Fatalf("row=%v", row)
	}
	if row["key"] != "sprint" {
		t.Fatalf("key=%v", row["key"])
	}
	clause := row["clauseNames"].([]any)
	if len(clause) != 2 || clause[0] != "cf[10020]" || clause[1] != "Sprint" {
		t.Fatalf("clauseNames=%v", clause)
	}
	schema := row["schema"].(map[string]any)
	if schema["custom"] != "com.pyxis.greenhopper.jira:gh-sprint" {
		t.Fatalf("schema=%v", schema)
	}
	if schema["customId"] != float64(10020) || schema["type"] != "array" {
		t.Fatalf("schema=%v", schema)
	}

	meta := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1/editmeta"))
	if _, has := meta["customfield_10020"]; has {
		t.Fatal("editmeta must not advertise the Sprint field")
	}
	cm := decode(t, authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003"))
	for _, raw := range cm["fields"].([]any) {
		if raw.(map[string]any)["fieldId"] == "customfield_10020" {
			t.Fatal("createmeta must not advertise the Sprint field")
		}
	}

	res := authPut(t, ts, "/rest/api/3/issue/TAP-1", map[string]any{
		"fields": map[string]any{"customfield_10020": []any{float64(1)}},
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT customfield_10020 status %d", res.StatusCode)
	}
	if _, has := decode(t, res)["errors"].(map[string]any)["customfield_10020"]; !has {
		t.Fatal("PUT must name the rejected field")
	}
}

// TestSprintFieldValueOrder: the array is every sprint oldest-first with
// the current one last — a done issue carried out of sprint 1 into
// sprint 2 lists both.
func TestSprintFieldValueOrder(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	s1 := createSprint(t, ts, "One", agileSprintDates())
	moveToSprint(t, ts, s1, "TAP-3") // done issue: survives the close
	authPost(t, ts, sprintURL(s1, ""), map[string]any(merge(agileSprintDates(), map[string]any{"state": "active"})))
	authPost(t, ts, sprintURL(s1, ""), map[string]any{"state": "closed"})

	s2 := createSprint(t, ts, "Two", nil)
	moveToSprint(t, ts, s2, "TAP-3")

	arr, ok := sprintField(t, ts, "TAP-3").([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("value=%v", arr)
	}
	first := arr[0].(map[string]any)
	second := arr[1].(map[string]any)
	if first["id"] != s1 || first["state"] != "closed" {
		t.Fatalf("oldest=%v", first)
	}
	if second["id"] != s2 || second["state"] != "future" {
		t.Fatalf("current=%v", second)
	}
	// Unset dates render null (Cloud's compact object keeps the keys),
	// never an invented value.
	if second["startDate"] != nil || second["endDate"] != nil {
		t.Fatalf("unset dates must be null: %v", second)
	}

	// The "sprint" name selects the same rendered field.
	res := authGet(t, ts, "/rest/api/3/issue/TAP-3?fields=sprint")
	if _, has := decode(t, res)["fields"].(map[string]any)["customfield_10020"]; !has {
		t.Fatal("fields=[sprint] must render customfield_10020")
	}
	// *navigable is the all-set.
	res = authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql": "key = TAP-3", "fields": []string{"*navigable"},
	})
	v := decode(t, res)
	fields := v["issues"].([]any)[0].(map[string]any)["fields"].(map[string]any)
	if _, has := fields["customfield_10020"]; !has {
		t.Fatalf("*navigable must include the sprint field: %v", fields)
	}
	if _, has := fields["summary"]; !has {
		t.Fatal("*navigable must behave like *all")
	}
}

func TestJQLSprintOperatorsAndFunctions(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	s1 := createSprint(t, ts, "Alpha", agileSprintDates())
	s2 := createSprint(t, ts, "Beta", nil)
	moveToSprint(t, ts, s1, "TAP-3", "TAP-1")
	moveToSprint(t, ts, s2, "TAP-2")

	// Operators: =, !=, in, not in, by id and by name.
	if got := keySet(t, searchKeys(t, ts, fmt.Sprintf("sprint = %d", int64(s1)))); !sameKeys(got, setOf("TAP-1", "TAP-3")) {
		t.Fatalf("sprint = id: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, `sprint = "Alpha"`)); !sameKeys(got, setOf("TAP-1", "TAP-3")) {
		t.Fatalf("sprint = name: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, fmt.Sprintf("sprint != %d", int64(s1)))); !sameKeys(got, setOf("TAP-2")) {
		t.Fatalf("sprint != id: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, fmt.Sprintf("sprint in (%d, %d)", int64(s1), int64(s2)))); !sameKeys(got, setOf("TAP-1", "TAP-2", "TAP-3")) {
		t.Fatalf("sprint in: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, fmt.Sprintf("sprint not in (%d)", int64(s1)))); !sameKeys(got, setOf("TAP-2")) {
		t.Fatalf("sprint not in: %v", got)
	}

	// Null tests: every issue is sprinted, then a backlog move empties one.
	if got := searchKeys(t, ts, "sprint is EMPTY"); len(got) != 0 {
		t.Fatalf("is EMPTY: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, "sprint is not EMPTY")); !sameKeys(got, setOf("TAP-1", "TAP-2", "TAP-3")) {
		t.Fatalf("is not EMPTY: %v", got)
	}
	moveToBacklog(t, ts, "TAP-2")
	if got := keySet(t, searchKeys(t, ts, "sprint is EMPTY")); !sameKeys(got, setOf("TAP-2")) {
		t.Fatalf("is EMPTY after backlog: %v", got)
	}

	// Functions: s1 is still future and holds TAP-1/TAP-3 (TAP-2 left for
	// the backlog above). Start it and they move to openSprints(), leaving
	// futureSprints empty.
	if got := keySet(t, searchKeys(t, ts, "sprint in futureSprints()")); !sameKeys(got, setOf("TAP-1", "TAP-3")) {
		t.Fatalf("futureSprints: %v", got)
	}
	authPost(t, ts, sprintURL(s1, ""), map[string]any(merge(agileSprintDates(), map[string]any{"state": "active"})))
	if got := keySet(t, searchKeys(t, ts, "sprint in openSprints()")); !sameKeys(got, setOf("TAP-1", "TAP-3")) {
		t.Fatalf("openSprints: %v", got)
	}
	if got := searchKeys(t, ts, "sprint in futureSprints()"); len(got) != 0 {
		t.Fatalf("futureSprints after start: %v", got)
	}
	authPost(t, ts, sprintURL(s1, ""), map[string]any{"state": "closed"})
	if got := searchKeys(t, ts, "sprint in openSprints()"); len(got) != 0 {
		t.Fatalf("openSprints after close: %v", got)
	}
	if got := keySet(t, searchKeys(t, ts, "sprint in closedSprints()")); !sameKeys(got, setOf("TAP-3")) {
		t.Fatalf("closedSprints: %v", got)
	}

	// Parse errors: unknown function names itself; a sprint function on
	// another field names the field restriction.
	for _, bad := range []struct{ jql, want string }{
		{"sprint in bogusSprints()", "bogusSprints"},
		{"status in openSprints()", "sprint field"},
		{"sprint in futureSprints(1)", "takes no arguments"},
	} {
		res := authPost(t, ts, "/rest/api/3/search/jql", map[string]any{"jql": bad.jql, "fields": []string{"key"}})
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("jql %q: status %d", bad.jql, res.StatusCode)
		}
		v := decode(t, res)
		msgs := v["errorMessages"].([]any)
		if len(msgs) == 0 || !contains(msgs[0].(string), bad.want) {
			t.Fatalf("jql %q: messages=%v", bad.jql, msgs)
		}
	}
}

func sameKeys(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}

// The agile prefix is not a blanket: shapes it does not serve stay honest
// 501s, never 404s.
func TestAgileElsewhereStillHonest(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	for _, path := range []string{
		"/rest/agile/1.0/epic/1",
		"/rest/agile/1.0/board/1/configuration",
	} {
		res := authGet(t, ts, path)
		if res.StatusCode != http.StatusNotImplemented {
			t.Fatalf("%s status %d", path, res.StatusCode)
		}
	}
	res := authPost(t, ts, "/rest/agile/1.0/sprint/1/backlog", map[string]any{})
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("DELETE-adjacent route status %d", res.StatusCode)
	}
}
