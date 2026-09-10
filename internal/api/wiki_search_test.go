package api_test

// GDK-1210: the wiki search pagination contract, over a fixture with more
// than one page — the one-page fixtures every other test uses never produce
// a `_links.next`, which is exactly how the two defects below survived.
//
//  1. `_links.next` must carry the cql percent-encoded. The link was built
//     by string concatenation, so a cql with spaces or quotes went in raw.
//     A client that re-parses the next URL (or a CQL containing `%`/`&`)
//     gets a corrupted query — measured: `space="R&D" …` parses back as
//     `space="R` with `invalid URL escape`. Cloud percent-encodes.
//  2. `order by lastmodified desc` must serve newest-first. The direction
//     was peeled off with the clause and never read, so desc answered asc.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// twoPageServer is tiny.yaml plus a second DOCS page stamped a day later,
// so `lastmodified` has an order to get wrong and limit=1 has a page 2.
func twoPageServer(t *testing.T) *httptest.Server {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Pages = append(doc.Pages, fixtures.Page{
		ID: "20002", Title: "Second page", Space: "DOCS", Version: 1,
		When: "2026-08-06T11:31:32.815Z", Author: "5b10a2844c20165700ede21g",
		Body: "added so pagination has a second row",
	})
	st := store.New(store.Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	t.Cleanup(func() { ts.Close() })
	return ts
}

// searchCQL GETs content/search with cql properly encoded as a query
// parameter — the same shape a Cloud-built client sends.
func searchCQL(t *testing.T, ts *httptest.Server, cql string) map[string]any {
	t.Helper()
	q := url.Values{}
	q.Set("cql", cql)
	res := authGet(t, ts, "/wiki/rest/api/content/search?"+q.Encode())
	defer res.Body.Close()
	var v map[string]any
	if err := jsonNewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("search %q: status %d body %v", cql, res.StatusCode, v)
	}
	return v
}

func jsonNewDecoder(r io.Reader) *json.Decoder { return json.NewDecoder(r) }

// TestCQLNextLinkEncodesCQL: the next link is a URL a client can follow
// verbatim. Cloud percent-encodes the cql it echoes into `_links.next`; the
// raw concatenation emitted `cql=space="DOCS" and type=page` — parseable
// only by a lenient client, and corrupt the moment the cql carries `%`/`&`.
func TestCQLNextLinkEncodesCQL(t *testing.T) {
	ts := twoPageServer(t)
	const cql = `space="DOCS" and type=page`

	// Request with limit=1 so a second page exists.
	q := url.Values{}
	q.Set("cql", cql)
	q.Set("limit", "1")
	res := authGet(t, ts, "/wiki/rest/api/content/search?"+q.Encode())
	defer res.Body.Close()
	var page map[string]any
	if err := jsonNewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	links, _ := page["_links"].(map[string]any)
	next, _ := links["next"].(string)
	if next == "" {
		t.Fatalf("no _links.next with 2 pages and limit=1: %v", links)
	}

	// Contract 1 — the link is encoded: no raw space or quote survives in
	// the query string, exactly like Cloud's next links.
	if strings.ContainsAny(next, " \"") {
		t.Fatalf("_links.next is not percent-encoded: %q", next)
	}
	// Contract 2 — the cql the link carries is the cql the client sent.
	u, err := url.Parse(next)
	if err != nil {
		t.Fatalf("next is not a parseable URL: %q (%v)", next, err)
	}
	got := u.Query().Get("cql")
	if got != cql {
		t.Fatalf("next cql = %q, want %q", got, cql)
	}
	if u.Query().Get("limit") != "1" {
		t.Fatalf("next limit = %q, want 1", u.Query().Get("limit"))
	}
	// Contract 3 — following it serves page 2 (the newer page, since desc
	// is asserted separately; here just: a different page). The link is
	// wiki-base-relative like the version-history next links; gadak's
	// nextPath resolves /rest/... against the /wiki base the same way.
	res2 := authGet(t, ts, "/wiki"+next)
	defer res2.Body.Close()
	var page2 map[string]any
	if err := jsonNewDecoder(res2.Body).Decode(&page2); err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("follow next: status %d body %v", res2.StatusCode, page2)
	}
	r1, _ := page["results"].([]any)
	r2, _ := page2["results"].([]any)
	if len(r2) == 0 || len(r1) == 0 {
		t.Fatalf("page1=%d page2=%d results — next did not advance", len(r1), len(r2))
	}
	id1, _ := r1[0].(map[string]any)["id"].(string)
	id2, _ := r2[0].(map[string]any)["id"].(string)
	if id1 == id2 {
		t.Fatalf("next served the same page again: %s", id1)
	}
}

// TestCQLOrderByDirectionServes: `order by lastmodified desc` answers
// newest-first. The direction used to be discarded with the clause, so both
// directions served ascending.
func TestCQLOrderByDirectionServes(t *testing.T) {
	ts := twoPageServer(t)
	for _, tc := range []struct {
		cql      string
		firstIDs []string // expected result ids in order (comment included when untyped… use type=page)
	}{
		{`space="DOCS" and type=page order by lastmodified asc`, []string{"20001", "20002"}},
		{`space="DOCS" and type=page order by lastmodified desc`, []string{"20002", "20001"}},
	} {
		v := searchCQL(t, ts, tc.cql)
		results, _ := v["results"].([]any)
		if len(results) != 2 {
			t.Fatalf("%s: results=%d want 2 (%v)", tc.cql, len(results), results)
		}
		for i, want := range tc.firstIDs {
			m, _ := results[i].(map[string]any)
			if m["id"] != want {
				t.Fatalf("%s: results[%d].id=%v want %v", tc.cql, i, m["id"], want)
			}
		}
	}
}

// TestCQLOrderByUnknownFieldIs400: ordering by anything but lastmodified
// has no sort behind it — serving it in id order would look like it worked.
// Same honesty rule as the clause set: HTTP 400, never a silent fallback.
func TestCQLOrderByUnknownFieldIs400(t *testing.T) {
	ts := twoPageServer(t)
	q := url.Values{}
	q.Set("cql", `space="DOCS" order by title`)
	res := authGet(t, ts, "/wiki/rest/api/content/search?"+q.Encode())
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("order by title: status %d, want 400", res.StatusCode)
	}
}
