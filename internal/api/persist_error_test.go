package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// TestDurablePersistFailureIsHTTP5xx: a write request against a durable
// store whose persist path cannot be replaced must not 2xx. FAIL-first
// GDK-346: before persist errors propagated, POST /issue returned 201.
func TestDurablePersistFailureIsHTTP5xx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	ts := httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
	defer ts.Close()

	res := authPost(t, ts, "/rest/api/3/issue", map[string]any{
		"fields": map[string]any{
			"project": map[string]any{"key": "TAP"},
			"summary": "durable-http-fail",
		},
	})
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", res.StatusCode)
	}
	v := decode(t, res)
	msgs, _ := v["errorMessages"].([]any)
	joined := ""
	for _, m := range msgs {
		s, _ := m.(string)
		joined += s
	}
	if !strings.Contains(joined, "in memory") || !strings.Contains(joined, "retry") {
		t.Fatalf("5xx body must say the change stayed in memory and will retry: %v", v)
	}
}
