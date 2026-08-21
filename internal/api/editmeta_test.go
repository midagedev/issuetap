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

func editMetaFields(t *testing.T, ts *httptest.Server, key string) map[string]any {
	t.Helper()
	v := decode(t, authGet(t, ts, "/rest/api/3/issue/"+key+"/editmeta"))
	fields, _ := v["fields"].(map[string]any)
	if fields == nil {
		t.Fatalf("editmeta missing fields: %v", v)
	}
	return fields
}

func TestEditMetaSystemFields(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	fields := editMetaFields(t, ts, "TAP-1")
	want := []string{"summary", "description", "labels", "priority", "assignee", "duedate", "parent", "issuetype", "fixVersions", "components"}
	for _, id := range want {
		raw, ok := fields[id]
		if !ok {
			t.Errorf("editmeta missing %s", id)
			continue
		}
		meta, _ := raw.(map[string]any)
		schema, _ := meta["schema"].(map[string]any)
		if schema["system"] != id {
			t.Errorf("%s schema.system=%v, want %s", id, schema["system"], id)
		}
		ops, _ := meta["operations"].([]any)
		if len(ops) == 0 {
			t.Errorf("%s has no operations", id)
		}
	}
	prio, _ := fields["priority"].(map[string]any)
	av, _ := prio["allowedValues"].([]any)
	if len(av) < 5 {
		t.Fatalf("priority.allowedValues=%v, want the catalog", prio["allowedValues"])
	}
	first, _ := av[0].(map[string]any)
	if first["id"] == "" || first["name"] == "" {
		t.Fatalf("priority allowedValues[0]=%v, want id+name", first)
	}
}

func TestEditMetaMissingIssue(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authGet(t, ts, "/rest/api/3/issue/NOPE-1/editmeta")
	if res.StatusCode != 404 {
		t.Fatalf("status %d, want 404", res.StatusCode)
	}
	res.Body.Close()
}

func TestEditMetaAdvertisedFieldsAccepted(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	fields := editMetaFields(t, ts, "TAP-2")
	// TAP-2 is a Task (level 0); TAP-1 is a Bug (also level 0). Cloud
	// rejects same-level parents, so the advertised parent PUT uses an Epic.
	epicRes := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project":   map[string]any{"key": "TAP"},
			"summary":   "epic for parent PUT",
			"issuetype": map[string]any{"id": "10000"},
		},
	})
	epicBody := decode(t, epicRes)
	epicKey, _ := epicBody["key"].(string)
	if epicRes.StatusCode != http.StatusCreated || epicKey == "" {
		t.Fatalf("need an Epic to PUT a legal parent, status %d body=%v", epicRes.StatusCode, epicBody)
	}
	// One valid PUT body per advertised system field. Custom option
	// fields are covered by the registry tests.
	puts := map[string]any{
		"summary":     "edited summary",
		"description": "edited description",
		"labels":      []string{"roundtrip"},
		"priority":    map[string]any{"id": "1"},
		"assignee":    map[string]any{"accountId": "5b10a2844c20165700ede22g"},
		"duedate":     "2026-09-01",
		"parent":      map[string]any{"key": epicKey},
		"issuetype":   map[string]any{"id": "10007"},
		"fixVersions": []any{map[string]any{"name": "2026.8"}},
		"components":  []any{map[string]any{"name": "Core"}},
	}
	for id := range fields {
		if _, ok := puts[id]; !ok {
			if len(id) >= len("customfield_") && id[:len("customfield_")] == "customfield_" {
				continue
			}
			t.Errorf("editmeta advertises %s but the contract table has no PUT body", id)
		}
	}
	for id, val := range puts {
		if _, ok := fields[id]; !ok {
			t.Errorf("editmeta does not advertise %s (cannot prove PUT acceptance)", id)
			continue
		}
		res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
			"fields": map[string]any{id: val},
		})
		if res.StatusCode != 204 {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Errorf("PUT %s status %d body=%s", id, res.StatusCode, body)
			continue
		}
		res.Body.Close()
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=*all"))
	out := got["fields"].(map[string]any)
	if out["summary"] != "edited summary" {
		t.Errorf("summary=%v", out["summary"])
	}
	if out["duedate"] != "2026-09-01" {
		t.Errorf("duedate=%v", out["duedate"])
	}
	if p, _ := out["priority"].(map[string]any); p["id"] != "1" {
		t.Errorf("priority=%v", out["priority"])
	}
	if a, _ := out["assignee"].(map[string]any); a["accountId"] != "5b10a2844c20165700ede22g" {
		t.Errorf("assignee=%v (must be first-class user, not a Custom overlay)", out["assignee"])
	}
}

func TestPutDuedateRejectedWhenMalformed(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{"duedate": "not-a-date"},
	})
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("status %d, want 400", res.StatusCode)
	}
	var v map[string]any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	errs, _ := v["errors"].(map[string]any)
	if errs["duedate"] == nil {
		t.Fatalf("expected errors.duedate, got %v", v)
	}
}

func TestPutDuedateIsFirstClassOnGET(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{"duedate": "2026-09-01"},
	})
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("PUT status %d", res.StatusCode)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=duedate"))
	fields := got["fields"].(map[string]any)
	if fields["duedate"] != "2026-09-01" {
		t.Fatalf("GET duedate=%v", fields["duedate"])
	}
	// Snapshot must persist it as the first-class key, not under custom.
	snap := decode(t, func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/fixtures/snapshot?format=json", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}())
	issues, _ := snap["issues"].([]any)
	var tap2 map[string]any
	for _, raw := range issues {
		m := raw.(map[string]any)
		if m["key"] == "TAP-2" {
			tap2 = m
		}
	}
	if tap2 == nil {
		t.Fatal("TAP-2 missing from snapshot")
	}
	if tap2["duedate"] != "2026-09-01" {
		t.Fatalf("snapshot duedate=%v", tap2["duedate"])
	}
	if custom, _ := tap2["custom"].(map[string]any); custom != nil {
		if _, ok := custom["duedate"]; ok {
			t.Fatalf("snapshot still has custom.duedate=%v", custom["duedate"])
		}
	}
}

func TestRegisteredOptionHTTP(t *testing.T) {
	doc := fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Users: []fixtures.User{{
			AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada", Email: "you@example.com",
		}},
		Issues: []fixtures.Issue{{Key: "TAP-1", Summary: "x", Project: "TAP"}},
		Fields: []fixtures.Field{{
			ID: "customfield_10050", Name: "Severity", Custom: true, Type: "option",
			Options: []fixtures.FieldOption{
				{ID: "10100", Value: "Sev1"},
				{ID: "10101", Value: "Sev2"},
			},
		}},
	}
	st := store.New(store.Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	defer ts.Close()

	meta := editMetaFields(t, ts, "TAP-1")
	cf, ok := meta["customfield_10050"].(map[string]any)
	if !ok {
		t.Fatal("editmeta missing customfield_10050")
	}
	schema, _ := cf["schema"].(map[string]any)
	if schema["type"] != "option" {
		t.Fatalf("schema.type=%v, want option", schema["type"])
	}
	av, _ := cf["allowedValues"].([]any)
	if len(av) != 2 {
		t.Fatalf("allowedValues=%v", cf["allowedValues"])
	}

	bad := authPut(t, ts, "/rest/api/3/issue/TAP-1", map[string]any{
		"fields": map[string]any{"customfield_10050": map[string]any{"id": "99999"}},
	})
	if bad.StatusCode != 400 {
		body, _ := io.ReadAll(bad.Body)
		bad.Body.Close()
		t.Fatalf("unknown option status %d body=%s", bad.StatusCode, body)
	}
	bad.Body.Close()

	good := authPut(t, ts, "/rest/api/3/issue/TAP-1", map[string]any{
		"fields": map[string]any{"customfield_10050": map[string]any{"id": "10100"}},
	})
	good.Body.Close()
	if good.StatusCode != 204 {
		t.Fatalf("known option status %d", good.StatusCode)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-1?fields=customfield_10050"))
	fields := got["fields"].(map[string]any)
	stored, _ := fields["customfield_10050"].(map[string]any)
	if stored["id"] != "10100" {
		t.Fatalf("stored %v", fields["customfield_10050"])
	}
}

func TestUnregisteredCustomFieldStillFree(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()
	res := authPut(t, ts, "/rest/api/3/issue/TAP-2", map[string]any{
		"fields": map[string]any{"customfield_99999": map[string]any{"value": "loose"}},
	})
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("status %d", res.StatusCode)
	}
	got := decode(t, authGet(t, ts, "/rest/api/3/issue/TAP-2?fields=customfield_99999"))
	fields := got["fields"].(map[string]any)
	if fields["customfield_99999"] == nil {
		t.Fatal("unregistered custom field dropped")
	}
}

func TestAdminDataExposesFieldRegistry(t *testing.T) {
	doc := fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Issues:   []fixtures.Issue{{Key: "TAP-1", Summary: "x", Project: "TAP"}},
		Fields: []fixtures.Field{{
			ID: "customfield_10050", Name: "Severity", Custom: true, Type: "option",
			Options: []fixtures.FieldOption{{ID: "10100", Value: "Sev1"}},
		}},
	}
	st := store.New(store.Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	defer ts.Close()
	v := decode(t, func() *http.Response {
		res, err := http.Get(ts.URL + "/api/data")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}())
	reg, _ := v["fieldRegistry"].([]any)
	found := false
	for _, raw := range reg {
		m := raw.(map[string]any)
		if m["id"] == "customfield_10050" {
			found = true
			if m["kind"] != "option" {
				t.Errorf("kind=%v", m["kind"])
			}
			opts, _ := m["options"].([]any)
			if len(opts) != 1 {
				t.Errorf("options=%v", m["options"])
			}
		}
	}
	if !found {
		t.Fatalf("fieldRegistry missing customfield_10050: %v", v["fieldRegistry"])
	}
}
