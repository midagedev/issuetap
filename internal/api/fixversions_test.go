package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

// GDK-516 HTTP surface: PUT update/fields for fixVersions and components
// must 204 onto the typed arrays (JQL hits) and unknown ids must 400
// errors.fixVersions / errors.components — not a Custom overlay 204.

func namedFromIssue(t *testing.T, ts *httptest.Server, key, field string) (id, name string) {
	t.Helper()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"?fields="+field))
	fields, _ := v["fields"].(map[string]any)
	arr, _ := fields[field].([]any)
	if len(arr) == 0 {
		t.Fatalf("%s.%s empty: %v", key, field, v)
	}
	row, _ := arr[0].(map[string]any)
	id, _ = row["id"].(string)
	name, _ = row["name"].(string)
	if id == "" || name == "" {
		t.Fatalf("%s.%s[0]=%v", key, field, row)
	}
	return id, name
}

func jqlKeys(t *testing.T, ts *httptest.Server, jql string) []string {
	t.Helper()
	v := decode(t, authPost(t, ts, "/rest/api/3/search/jql", map[string]any{
		"jql":    jql,
		"fields": []string{"key"},
	}))
	raw, _ := v["issues"].([]any)
	out := make([]string, 0, len(raw))
	for _, row := range raw {
		m, _ := row.(map[string]any)
		k, _ := m["key"].(string)
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

func hasIssueKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestPutFixVersionsAddByIDThenJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	id, name := namedFromIssue(t, ts, "TAP-1", "fixVersions")
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"update": map[string]any{
			"fixVersions": []any{
				map[string]any{"add": map[string]any{"id": id}},
			},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusNoContent {
		t.Fatalf("PUT update.fixVersions add status %d body=%v", status, body)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=fixVersions"))
	fields, _ := got["fields"].(map[string]any)
	arr, _ := fields["fixVersions"].([]any)
	if len(arr) != 1 {
		t.Fatalf("GET fixVersions=%v", fields["fixVersions"])
	}
	row, _ := arr[0].(map[string]any)
	if row["id"] != id || row["name"] != name {
		t.Fatalf("GET row=%v want id=%s name=%s", row, id, name)
	}
	keys := jqlKeys(t, ts, `fixVersion = "`+name+`"`)
	if !hasIssueKey(keys, "TAP-2") {
		t.Fatalf("JQL fixVersion = %q returned %v, want TAP-2", name, keys)
	}
}

func TestPutFixVersionsFieldsReplaceThenJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	_, name := namedFromIssue(t, ts, "TAP-1", "fixVersions")
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"fixVersions": []any{map[string]any{"name": name}},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusNoContent {
		t.Fatalf("PUT fields.fixVersions status %d body=%v", status, body)
	}
	keys := jqlKeys(t, ts, `fixVersion = "`+name+`"`)
	if !hasIssueKey(keys, "TAP-2") {
		t.Fatalf("JQL after fields replace returned %v, want TAP-2", keys)
	}
}

func TestPutComponentsAddByNameThenJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	_, name := namedFromIssue(t, ts, "TAP-1", "components")
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"update": map[string]any{
			"components": []any{
				map[string]any{"add": map[string]any{"name": name}},
			},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusNoContent {
		t.Fatalf("PUT update.components add status %d body=%v", status, body)
	}
	keys := jqlKeys(t, ts, `component = "`+name+`"`)
	if !hasIssueKey(keys, "TAP-2") {
		t.Fatalf("JQL component returned %v, want TAP-2", keys)
	}
}

func TestPutComponentsFieldsReplaceThenJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	id, name := namedFromIssue(t, ts, "TAP-1", "components")
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"components": []any{map[string]any{"id": id}},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusNoContent {
		t.Fatalf("PUT fields.components status %d body=%v", status, body)
	}
	keys := jqlKeys(t, ts, `component = "`+name+`"`)
	if !hasIssueKey(keys, "TAP-2") {
		t.Fatalf("JQL after fields replace returned %v, want TAP-2", keys)
	}
}

func TestPutFixVersionsUnknownIDIs400(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"update": map[string]any{
			"fixVersions": []any{
				map[string]any{"add": map[string]any{"id": "99999"}},
			},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown id status %d body=%v, want 400", status, body)
	}
	errs, _ := body["errors"].(map[string]any)
	if errs == nil || errs["fixVersions"] == nil {
		t.Fatalf("want errors.fixVersions, got %v", body)
	}
}

func TestPutFixVersionsFieldsUnknownIDIs400(t *testing.T) {
	// fields replace of an unknown id used to 204 into Custom.
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"fixVersions": []any{map[string]any{"id": "99999"}},
		},
	})
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("fields unknown id status %d body=%s, want 400", res.StatusCode, raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	errs, _ := body["errors"].(map[string]any)
	if errs == nil || errs["fixVersions"] == nil {
		t.Fatalf("want errors.fixVersions, got %v", body)
	}
}

func TestPutUnknownSystemKeyStillCustom(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{
			"versions": []any{map[string]any{"name": "stay-custom"}},
		},
	})
	status, body := decodeStatus(t, res)
	if status != http.StatusNoContent {
		t.Fatalf("unknown key status %d body=%v, want 204 (Custom)", status, body)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=versions"))
	fields, _ := got["fields"].(map[string]any)
	raw, _ := json.Marshal(fields["versions"])
	if !strings.Contains(string(raw), "stay-custom") {
		t.Fatalf("GET versions=%s, want Custom overlay of the write", raw)
	}
}

// GDK-581: POST /issue with fixVersions/components must advertise those
// fields on editmeta/createmeta and make JQL hit the created issue.
func TestCreateFixVersionsComponentsAdvertisedAndJQL(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	vid, vname := namedFromIssue(t, ts, "TAP-1", "fixVersions")
	cid, cname := namedFromIssue(t, ts, "TAP-1", "components")

	createFields := decode(t, authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003"))
	list, _ := createFields["fields"].([]any)
	seen := map[string]map[string]any{}
	for _, raw := range list {
		m, _ := raw.(map[string]any)
		id, _ := m["fieldId"].(string)
		seen[id] = m
	}
	for _, id := range []string{"fixVersions", "components"} {
		meta, ok := seen[id]
		if !ok {
			t.Fatalf("createmeta missing %s; ids=%v", id, fieldIDs(seen))
		}
		av, _ := meta["allowedValues"].([]any)
		if len(av) == 0 {
			t.Fatalf("createmeta %s.allowedValues empty: %v", id, meta)
		}
	}

	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":     map[string]any{"key": "TAP"},
			"summary":     "created with named lists",
			"issuetype":   map[string]any{"id": "10003"},
			"fixVersions": []any{map[string]any{"id": vid}},
			"components":  []any{map[string]any{"name": cname}},
		},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("POST create status %d body=%s", res.StatusCode, body)
	}
	created := decode(t, res)
	key, _ := created["key"].(string)
	if key == "" {
		t.Fatalf("POST create missing key: %v", created)
	}

	edit := editMetaFields(t, ts, key)
	for _, id := range []string{"fixVersions", "components"} {
		raw, ok := edit[id]
		if !ok {
			t.Fatalf("editmeta missing %s", id)
		}
		meta, _ := raw.(map[string]any)
		av, _ := meta["allowedValues"].([]any)
		if len(av) == 0 {
			t.Fatalf("editmeta %s.allowedValues empty: %v", id, meta)
		}
	}

	got := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"?fields=fixVersions,components"))
	fields, _ := got["fields"].(map[string]any)
	fv, _ := fields["fixVersions"].([]any)
	if len(fv) != 1 {
		t.Fatalf("GET fixVersions=%v", fields["fixVersions"])
	}
	row, _ := fv[0].(map[string]any)
	if row["id"] != vid || row["name"] != vname {
		t.Fatalf("GET fixVersions[0]=%v want id=%s name=%s", row, vid, vname)
	}
	comp, _ := fields["components"].([]any)
	if len(comp) != 1 {
		t.Fatalf("GET components=%v", fields["components"])
	}
	crow, _ := comp[0].(map[string]any)
	if crow["id"] != cid || crow["name"] != cname {
		t.Fatalf("GET components[0]=%v want id=%s name=%s", crow, cid, cname)
	}

	if !hasIssueKey(jqlKeys(t, ts, `fixVersion = "`+vname+`"`), key) {
		t.Fatalf("JQL fixVersion missed created %s", key)
	}
	if !hasIssueKey(jqlKeys(t, ts, `component = "`+cname+`"`), key) {
		t.Fatalf("JQL component missed created %s", key)
	}
}
