package issuetap_test

// Embedding smoke gate: an external program uses only the public root
// package — NewEmbedded, ServeHTTP, Close — and its data survives a
// restart via the persistence file.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	issuetap "github.com/midagedev/issuetap"
)

// Compiled as an external test package on purpose: it may only use the
// public identifiers an embedding program would import.

func authReq(t *testing.T, h http.Handler, method, path string, body []byte, ct string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, path, bytes.NewReader(body))
	req.SetBasicAuth("embedder@example.com", "embed-token")
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// TestEmbeddedRestartSmoke is the round's embedding gate: POST an issue,
// Close (restart), reopen from the same persistence file, GET the issue.
func TestEmbeddedRestartSmoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")

	e1, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{
		PersistPath:     path,
		PersistDebounce: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "EMB"},
			"summary": "created in the first process",
		},
	})
	code, body := authReq(t, e1, http.MethodPost, "/rest/api/3/issue", payload, "application/json")
	if code != http.StatusCreated {
		t.Fatalf("POST /issue: %d: %s", code, body)
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Key == "" {
		t.Fatalf("POST /issue body: %s (%v)", body, err)
	}
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	// "Restart": a fresh Embedded over the same file.
	e2, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	code, body = authReq(t, e2, http.MethodGet, "/rest/api/3/issue/"+created.Key, nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s after restart: %d: %s", created.Key, code, body)
	}
	var iss struct {
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(body, &iss); err != nil {
		t.Fatal(err)
	}
	if iss.Fields.Summary != "created in the first process" {
		t.Fatalf("summary after restart = %q", iss.Fields.Summary)
	}
}

// TestEmbeddedFixtureBytesAndSnapshot covers the seed-from-bytes path and
// the Snapshot export.
func TestEmbeddedFixtureBytesAndSnapshot(t *testing.T) {
	e, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{
		FixtureBytes: []byte("issues:\n  - {key: BYT-1, summary: from bytes}\n"),
		Locale:       "ko",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	code, body := authReq(t, e, http.MethodGet, "/rest/api/3/issue/BYT-1", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET BYT-1: %d: %s", code, body)
	}
	snap, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(snap, []byte("BYT-1")) || !bytes.Contains(snap, []byte("from bytes")) {
		t.Fatalf("snapshot missing seeded issue:\n%s", snap)
	}
}

// TestEmbeddedPersistFileSupersedesFixture: when the persistence file
// exists, it wins over the fixture — the restart continues instead of
// reseeding.
func TestEmbeddedPersistFileSupersedesFixture(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	e1, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{
		FixtureBytes: []byte("issues:\n  - {key: OLD-1, summary: seed}\n"),
		PersistPath:  path, PersistDebounce: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "NEW"},
			"summary": "only in persisted state",
		},
	})
	if code, body := authReq(t, e1, http.MethodPost, "/rest/api/3/issue", payload, "application/json"); code != http.StatusCreated {
		t.Fatalf("POST: %d: %s", code, body)
	}
	if err := e1.Close(); err != nil {
		t.Fatal(err)
	}

	e2, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{
		FixtureBytes: []byte("issues:\n  - {key: OLD-1, summary: seed}\n"),
		PersistPath:  path,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if code, _ := authReq(t, e2, http.MethodGet, "/rest/api/3/issue/NEW-1", nil, ""); code != http.StatusOK {
		t.Fatalf("persisted NEW-1 missing after restart: %d", code)
	}
}

// TestEmbeddedHealthz: the handler answers /healthz without auth.
func TestEmbeddedHealthz(t *testing.T) {
	e, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/healthz", nil)
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"ok":true`)) {
		t.Fatalf("healthz: %d: %s", rec.Code, rec.Body)
	}
}
