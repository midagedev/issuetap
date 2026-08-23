package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// TestDurablePersistFailureIsHTTP5xx: YAML write-through mapped a failed
// rename to HTTP 500. Stage 3's working copy is the SQLite file — a
// directory PersistPath fails at Open, and a live POST commits before 2xx.
func TestDurablePersistFailureIsHTTP5xx(t *testing.T) {
	dirPath := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: dirPath, PersistDebounce: -1}); err == nil {
		t.Fatal("expected Open of a directory PersistPath to fail")
	}

	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
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
	defer ts.Close()

	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "TAP"},
			"summary": "durable-http-ok",
		},
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status %d, want 201 (SQL persist commits before ACK)", res.StatusCode)
	}
	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	found := false
	for _, iss := range st2.Snapshot().Issues {
		if iss.Summary == "durable-http-ok" {
			found = true
		}
	}
	if !found {
		t.Fatal("POST /issue 2xx did not persist before return")
	}
}
