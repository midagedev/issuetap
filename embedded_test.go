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

// namesOf decodes a JSON array of catalog rows into id→name.
func namesOf(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("bad catalog JSON: %v: %s", err, body)
	}
	out := map[string]string{}
	for _, m := range arr {
		out[m["id"].(string)] = m["name"].(string)
	}
	return out
}

// TestEmbeddedLocaleCloudFidelity (gadak GDK-597): the embedded role is a
// real tracker, so a ko workspace serves what the live ko_KR site served —
// Korean status and issue-type names, English priority names. The serve
// role's priority trap (localized priorities) stays available to an
// embedder via PriorityLocaleTrap.
func TestEmbeddedLocaleCloudFidelity(t *testing.T) {
	e, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{Locale: "ko"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	code, body := authReq(t, e, http.MethodGet, "/rest/api/3/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /status: %d: %s", code, body)
	}
	if got := namesOf(t, body)["3"]; got != "진행 중" {
		t.Fatalf("ko status 3 = %q — status names must localize", got)
	}

	code, body = authReq(t, e, http.MethodGet, "/rest/api/3/priority", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /priority: %d: %s", code, body)
	}
	prios := namesOf(t, body)
	if got := prios["2"]; got != "High" {
		t.Fatalf("ko priority 2 = %q — Cloud fidelity keeps priority names English", got)
	}

	// The trap opt-in restores the serve behavior.
	trap, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{Locale: "ko", PriorityLocaleTrap: true})
	if err != nil {
		t.Fatal(err)
	}
	defer trap.Close()
	code, body = authReq(t, trap, http.MethodGet, "/rest/api/3/priority", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /priority (trap): %d: %s", code, body)
	}
	if got := namesOf(t, body)["2"]; got != "높음" {
		t.Fatalf("trap priority 2 = %q — the serve trap must stay reachable", got)
	}
}

// TestEmbeddedSetLocaleRuntime (gadak GDK-597): a config change reaches a
// live embedded store without dropping the persist lock — the gadak-serve
// path. Status names follow; priority names stay English (the role is
// fixed at open).
func TestEmbeddedSetLocaleRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	e, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()

	code, body := authReq(t, e, http.MethodGet, "/rest/api/3/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /status: %d: %s", code, body)
	}
	if got := namesOf(t, body)["3"]; got != "In Progress" {
		t.Fatalf("en status 3 = %q", got)
	}

	if err := e.SetLocale("ko"); err != nil {
		t.Fatal(err)
	}
	code, body = authReq(t, e, http.MethodGet, "/rest/api/3/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /status after SetLocale: %d: %s", code, body)
	}
	if got := namesOf(t, body)["3"]; got != "진행 중" {
		t.Fatalf("status 3 after SetLocale(ko) = %q", got)
	}
	code, body = authReq(t, e, http.MethodGet, "/rest/api/3/priority", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /priority after SetLocale: %d: %s", code, body)
	}
	if got := namesOf(t, body)["2"]; got != "High" {
		t.Fatalf("priority 2 after SetLocale(ko) = %q — role must not follow the runtime locale", got)
	}

	// The locale change persists like any other mutation.
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e2, err := issuetap.NewEmbedded(issuetap.EmbeddedConfig{PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	code, body = authReq(t, e2, http.MethodGet, "/rest/api/3/status", nil, "")
	if code != http.StatusOK {
		t.Fatalf("GET /status after restart: %d: %s", code, body)
	}
	if got := namesOf(t, body)["3"]; got != "진행 중" {
		t.Fatalf("status 3 after restart = %q — SetLocale must persist", got)
	}
}
