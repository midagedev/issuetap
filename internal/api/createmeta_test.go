package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

func fieldByID(t *testing.T, fields []any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for i, raw := range fields {
		m, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("fields[%d] is %T, want object (list of field metas, not an editmeta map)", i, raw)
		}
		id, _ := m["fieldId"].(string)
		if id == "" {
			t.Fatalf("fields[%d] missing fieldId: %v", i, m)
		}
		out[id] = m
	}
	return out
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func fieldIDs(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCreateMetaSiblingStillProjects(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/createmeta"))
	projects, _ := v["projects"].([]any)
	if len(projects) == 0 {
		t.Fatalf("GET /issue/createmeta lost projects: %v", v)
	}
}

func TestCreateMetaFieldsShape(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003")
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("status %d, want 200 body=%s", res.StatusCode, body)
	}
	v := decode(t, res)
	fields, ok := v["fields"].([]any)
	if !ok {
		t.Fatalf("fields is %T, want a list (editmeta is a map; this endpoint is not)", v["fields"])
	}
	if _, ok := v["startAt"]; !ok {
		t.Fatalf("missing startAt: %v", v)
	}
	if _, ok := v["maxResults"]; !ok {
		t.Fatalf("missing maxResults: %v", v)
	}
	if _, ok := v["total"]; !ok {
		t.Fatalf("missing total: %v", v)
	}
	if int(v["total"].(float64)) != len(fields) {
		t.Fatalf("unpaged total=%v len(fields)=%d", v["total"], len(fields))
	}
	byID := fieldByID(t, fields)

	wantRequired := map[string]bool{
		"project": true, "summary": true, "issuetype": true, "reporter": true,
	}
	wantDefault := map[string]bool{
		"reporter": true, "issuetype": true, "priority": true,
	}
	for id, req := range wantRequired {
		f, ok := byID[id]
		if !ok {
			t.Errorf("missing %s", id)
			continue
		}
		if asBool(f["required"]) != req {
			t.Errorf("%s required=%v want %v", id, f["required"], req)
		}
	}
	for id, f := range byID {
		got := asBool(f["hasDefaultValue"])
		if wantDefault[id] != got {
			t.Errorf("%s hasDefaultValue=%v want %v", id, got, wantDefault[id])
		}
	}
	optional := []string{"description", "labels", "assignee", "duedate", "parent"}
	for _, id := range optional {
		f, ok := byID[id]
		if !ok {
			t.Errorf("missing optional %s", id)
			continue
		}
		if asBool(f["required"]) {
			t.Errorf("%s required=true, want false", id)
		}
		if asBool(f["hasDefaultValue"]) {
			t.Errorf("%s hasDefaultValue=true, want false", id)
		}
	}
	if byID["summary"]["name"] != "Summary" {
		t.Errorf("summary name=%v want Summary", byID["summary"]["name"])
	}
	if byID["issuetype"]["name"] != "Issue Type" {
		t.Errorf("issuetype name=%v want Issue Type", byID["issuetype"]["name"])
	}
	prio, ok := byID["priority"]
	if !ok {
		t.Fatal("missing priority")
	}
	av, _ := prio["allowedValues"].([]any)
	if len(av) < 5 {
		t.Errorf("priority.allowedValues=%v, want the catalog", prio["allowedValues"])
	}
	it, ok := byID["issuetype"]
	if !ok {
		t.Fatal("missing issuetype")
	}
	itAV, _ := it["allowedValues"].([]any)
	if len(itAV) < 1 {
		t.Errorf("issuetype.allowedValues empty: %v", it["allowedValues"])
	}
	cf, ok := byID["customfield_10050"]
	if !ok {
		t.Fatalf("missing customfield_10050 (tiny.yaml registry): ids=%v", fieldIDs(byID))
	}
	if asBool(cf["required"]) || asBool(cf["hasDefaultValue"]) {
		t.Errorf("customfield_10050 required/default = %v / %v", cf["required"], cf["hasDefaultValue"])
	}
	if cf["name"] != "Severity" {
		t.Errorf("customfield_10050 name=%v want Severity", cf["name"])
	}
}

func TestCreateMetaFieldsByProjectID(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/createmeta/10000/issuetypes/10003")
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("project id lookup status %d body=%s", res.StatusCode, body)
	}
	res.Body.Close()
}

func TestCreateMetaFieldsPagination(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	fullRes := authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003")
	if fullRes.StatusCode != 200 {
		body, _ := io.ReadAll(fullRes.Body)
		fullRes.Body.Close()
		t.Fatalf("full page status %d body=%s", fullRes.StatusCode, body)
	}
	full := decode(t, fullRes)
	all, _ := full["fields"].([]any)
	if len(all) < 4 {
		t.Fatalf("need enough fields to page, got %d", len(all))
	}
	total := int(full["total"].(float64))

	var collected []any
	startAt := 0
	pages := 0
	for {
		path := "/rest/api/3/issue/createmeta/TAP/issuetypes/10003?startAt=" +
			strconv.Itoa(startAt) + "&maxResults=2"
		res := authGet(t, ts, path)
		if res.StatusCode != 200 {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Fatalf("page startAt=%d status %d body=%s", startAt, res.StatusCode, body)
		}
		page := decode(t, res)
		if int(page["startAt"].(float64)) != startAt {
			t.Errorf("startAt=%v want %d", page["startAt"], startAt)
		}
		if int(page["maxResults"].(float64)) != 2 {
			t.Errorf("maxResults=%v want 2", page["maxResults"])
		}
		if int(page["total"].(float64)) != total {
			t.Errorf("total=%v want %d", page["total"], total)
		}
		chunk, _ := page["fields"].([]any)
		collected = append(collected, chunk...)
		pages++
		if len(chunk) == 0 {
			break
		}
		startAt += len(chunk)
		if startAt >= total {
			break
		}
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages < 2 {
		t.Fatalf("pages=%d — a one-page implementation that ignores startAt/maxResults fails here", pages)
	}
	if len(collected) != len(all) {
		t.Fatalf("paged len=%d full len=%d", len(collected), len(all))
	}
	for i := range all {
		a, _ := all[i].(map[string]any)
		b, _ := collected[i].(map[string]any)
		if a["fieldId"] != b["fieldId"] {
			t.Errorf("index %d full=%v paged=%v", i, a["fieldId"], b["fieldId"])
		}
	}
}

func TestCreateMetaFieldsPaginationEmptyPage(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003?startAt=0&maxResults=2")
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("status %d body=%s", res.StatusCode, body)
	}
	first := decode(t, res)
	total := int(first["total"].(float64))
	res = authGet(t, ts, "/rest/api/3/issue/createmeta/TAP/issuetypes/10003?startAt="+strconv.Itoa(total)+"&maxResults=2")
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("empty page status %d body=%s", res.StatusCode, body)
	}
	page := decode(t, res)
	fields, _ := page["fields"].([]any)
	if len(fields) != 0 {
		t.Fatalf("startAt=total fields=%v, want empty list", fields)
	}
}

func TestCreateMetaFieldsMissingProjectAndType(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	for _, path := range []string{
		"/rest/api/3/issue/createmeta/NOSUCH/issuetypes/10003",
		"/rest/api/3/issue/createmeta/TAP/issuetypes/99999",
	} {
		res := authGet(t, ts, path)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode == http.StatusNotImplemented {
			t.Errorf("%s status 501 — this route is implemented, missing resources are 404", path)
		}
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s status %d want 404 body=%s", path, res.StatusCode, body)
		}
		if strings.Contains(string(body), "unsupported_endpoint") {
			t.Errorf("%s body claims unsupported_endpoint: %s", path, body)
		}
	}
}

func TestCreateMetaFieldsRoundTrip(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "TAP"},
			"summary":   "created via advertised required set",
			"issuetype": map[string]any{"id": "10003"},
			"reporter":  map[string]any{"accountId": "5b10a2844c20165700ede21g"},
		},
	})
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("POST with advertised required status %d body=%s", res.StatusCode, body)
	}
	created := decode(t, res)
	if created["key"] == "" {
		t.Fatalf("POST succeeded without key: %v", created)
	}
}

func TestCreateIssueRejectsEmptySummary(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	for name, fields := range map[string]map[string]any{
		"missing": {"project": map[string]any{"key": "TAP"}},
		"empty":   {"project": map[string]any{"key": "TAP"}, "summary": ""},
	} {
		t.Run(name, func(t *testing.T) {
			res := authPost(t, ts, "/rest/api/3/issue", map[string]any{"fields": fields})
			defer res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(res.Body)
				t.Fatalf("status %d want 400 body=%s", res.StatusCode, body)
			}
			var v map[string]any
			if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
				t.Fatal(err)
			}
			errs, _ := v["errors"].(map[string]any)
			if errs["summary"] == nil {
				t.Fatalf("expected errors.summary (Jira per-field 400), got %v", v)
			}
		})
	}
}
