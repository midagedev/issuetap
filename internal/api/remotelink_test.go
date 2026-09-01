package api_test

// Remote issue links (gadak GDK-1032): Cloud's
// /rest/api/{v}/issue/{key}/remotelink trimmed to the pointer fields.
// Contract pinned here: create 201 / upsert-by-globalId 200, list shape,
// delete 204, 404s, persist reload, snapshot round-trip.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func remoteLinkPayload(globalID, relationship, url, title string) map[string]any {
	obj := map[string]any{"url": url}
	if title != "" {
		obj["title"] = title
	}
	body := map[string]any{"object": obj}
	if globalID != "" {
		body["globalId"] = globalID
	}
	if relationship != "" {
		body["relationship"] = relationship
	}
	return body
}

func listRemoteLinks(t *testing.T, ts *httptest.Server, key string) []map[string]any {
	t.Helper()
	res := authGet(t, ts, "/rest/api/3/issue/"+key+"/remotelink")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET remotelink status %d, want 200", res.StatusCode)
	}
	raw := decodeArr(t, res)
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("remotelink[%d] is %T", i, item)
		}
		out = append(out, m)
	}
	return out
}

func authDelete(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.SetBasicAuth("you@example.com", "issuetap")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRemoteLinkCreateListUpsertDelete(t *testing.T) {
	ts := testServer(t, locale.EN, dialect.Cloud)
	defer ts.Close()

	// Create (201), the second write to the same globalId updates (200).
	res := authPost(t, ts, "/rest/api/3/issue/TAP-1/remotelink",
		remoteLinkPayload("gadak:ws=work:NMA-9", "relates to", "gadak://work/NMA-9", "NMA-9"))
	if res.StatusCode != http.StatusCreated {
		status, msgs := jiraErrorMessages(t, res)
		t.Fatalf("POST remotelink status %d, want 201; %s", status, msgs)
	}
	created := decode(t, res)
	if created["id"] == nil {
		t.Fatalf("create response missing id: %v", created)
	}

	res = authPost(t, ts, "/rest/api/3/issue/TAP-1/remotelink",
		remoteLinkPayload("gadak:ws=work:NMA-9", "blocks", "gadak://work/NMA-9", "NMA-9 renamed"))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upsert by globalId status %d, want 200", res.StatusCode)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()

	links := listRemoteLinks(t, ts, "TAP-1")
	if len(links) != 1 {
		t.Fatalf("after upsert want 1 link, got %d: %v", len(links), links)
	}
	obj, _ := links[0]["object"].(map[string]any)
	if obj == nil || obj["url"] != "gadak://work/NMA-9" || obj["title"] != "NMA-9 renamed" {
		t.Fatalf("object shape: %v", links[0])
	}
	if links[0]["relationship"] != "blocks" || links[0]["globalId"] != "gadak:ws=work:NMA-9" {
		t.Fatalf("upsert did not replace fields: %v", links[0])
	}

	// No globalId → every POST creates.
	res = authPost(t, ts, "/rest/api/3/issue/TAP-1/remotelink",
		remoteLinkPayload("", "", "https://example.com/doc", ""))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("globalId-less create status %d, want 201", res.StatusCode)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	links = listRemoteLinks(t, ts, "TAP-1")
	if len(links) != 2 {
		t.Fatalf("want 2 links, got %d", len(links))
	}
	// A URL without a title serves the URL as title.
	var urlOnly map[string]any
	for _, l := range links {
		o, _ := l["object"].(map[string]any)
		if o != nil && o["url"] == "https://example.com/doc" {
			urlOnly = l
		}
	}
	if urlOnly == nil {
		t.Fatalf("second link missing: %v", links)
	}
	if o := urlOnly["object"].(map[string]any); o["title"] != "https://example.com/doc" {
		t.Fatalf("title fallback: %v", urlOnly)
	}

	// Delete by id (204), then it is gone; deleting again is 404.
	id := fmt.Sprint(urlOnly["id"])
	res = authDelete(t, ts, "/rest/api/3/issue/TAP-1/remotelink/"+id)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", res.StatusCode)
	}
	res.Body.Close()
	if got := listRemoteLinks(t, ts, "TAP-1"); len(got) != 1 {
		t.Fatalf("after delete want 1 link, got %d", len(got))
	}
	res = authDelete(t, ts, "/rest/api/3/issue/TAP-1/remotelink/"+id)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("second DELETE status %d, want 404", res.StatusCode)
	}
	res.Body.Close()

	// Refusals: missing url is 400, unknown issue is 404.
	res = authPost(t, ts, "/rest/api/3/issue/TAP-1/remotelink", map[string]any{"object": map[string]any{}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing url status %d, want 400", res.StatusCode)
	}
	res.Body.Close()
	res = authPost(t, ts, "/rest/api/3/issue/TAP-999/remotelink",
		remoteLinkPayload("", "", "https://example.com", ""))
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown issue status %d, want 404", res.StatusCode)
	}
	res.Body.Close()
}

func TestRemoteLinkSurvivesPersistReloadAndSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
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

	res := authPost(t, ts, "/rest/api/3/issue/TAP-1/remotelink",
		remoteLinkPayload("gadak:ws=work:NMA-9", "relates to", "gadak://work/NMA-9", "NMA-9"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST status %d, want 201", res.StatusCode)
	}
	res.Body.Close()
	ts.Close()

	// The snapshot carries the link (fixture round-trip).
	snap := st.Snapshot()
	b, err := fixtures.MarshalYAML(snap)
	if err != nil {
		t.Fatal(err)
	}
	back, err := fixtures.Parse(b, ".yaml")
	if err != nil {
		t.Fatalf("snapshot must parse back: %v", err)
	}
	found := false
	for _, is := range back.Issues {
		if is.Key != "TAP-1" {
			continue
		}
		for _, rl := range is.RemoteLinks {
			if rl.URL == "gadak://work/NMA-9" && rl.GlobalID == "gadak:ws=work:NMA-9" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("snapshot lost the remote link")
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	iss := st2.Issue("TAP-1")
	if iss == nil || len(iss.RemoteLinks) != 1 || iss.RemoteLinks[0].URL != "gadak://work/NMA-9" {
		t.Fatalf("remote link lost across persist reload: %+v", iss)
	}
}
