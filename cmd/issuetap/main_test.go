package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func TestServeLocaleFlagWinsFixture(t *testing.T) {
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	cfg.Locale = locale.KO
	st, _, err := loadServeGraph(cfg, "", true, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Locale() != locale.KO {
		t.Fatalf("serve --locale ko --fixture tiny.yaml: locale=%s, want ko (fixture locale:en must not win)", st.Locale())
	}
}

func TestServeFixtureLocaleWhenFlagOmitted(t *testing.T) {
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	cfg.Locale = locale.EN
	st, _, err := loadServeGraph(cfg, "", false, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Locale() != locale.EN {
		t.Fatalf("fixture locale:en should remain when --locale is omitted, got %s", st.Locale())
	}
}

func TestServePersistFileSupersedesFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.yaml")
	seed := "issues:\n  - key: PER-1\n    summary: persisted state\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	// The persisted graph must win: PER-1 present, fixture TAP issues gone.
	loaded, _, err := loadServeGraph(cfg, "", false, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Issue("PER-1") == nil {
		t.Fatal("persisted issue PER-1 not loaded")
	}
	if loaded.Issue("TAP-1") != nil {
		t.Fatal("fixture TAP-1 loaded despite existing persist file; persist must supersede --fixture")
	}
}

// GDK-584: serve --persist default must write the file before the mutation
// HTTP 2xx returns. Reloading the persist file (without Flush/Close of the
// live store) must see the created issue.
func TestServePersistDefaultWritesBeforeHTTP2xx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	st, _, err := loadServeGraph(cfg, "", false, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	defer ts.Close()

	body, err := json.Marshal(map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "TAP"},
			"summary": "durable-before-return",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/rest/api/3/issue", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST /issue status %d, want 201", res.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	key, _ := created["key"].(string)
	if key == "" {
		t.Fatalf("POST missing key: %v", created)
	}

	// Reload from disk immediately. Do not Flush/Close the live store first
	// — that would hide a debounce window.
	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.Issue(key) == nil {
		t.Fatalf("persist file missing %s at HTTP 2xx; debounce wrote after ACK", key)
	}
	if got := st2.Issue(key).Summary; got != "durable-before-return" {
		t.Fatalf("reloaded summary=%q", got)
	}
}

// GDK-584: --persist-debounce is the lab-only delayed write. A 2xx must
// not imply the file already contains the mutation.
func TestServePersistDebounceFlagDelaysWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	st, _, err := loadServeGraph(cfg, "", false, path, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	defer ts.Close()

	body, err := json.Marshal(map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "TAP"},
			"summary": "lab-debounce",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/rest/api/3/issue", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST /issue status %d, want 201", res.StatusCode)
	}
	var created map[string]any
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	key, _ := created["key"].(string)
	if key == "" {
		t.Fatalf("POST missing key: %v", created)
	}
	st2, err := store.Open(store.Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.Issue(key) != nil {
		t.Fatalf("lab debounce wrote %s before the quiet window; persist must wait", key)
	}
}
