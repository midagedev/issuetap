package conformance

// Gadak conformance is the v0 acceptance test: point gadak at issuetap
// and sync. If this fails, the emulation is not real.

import (
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

func gadakSrc(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ISSUETAP_GADAK_SRC"); p != "" {
		return p
	}
	candidates := []string{
		filepath.Join(fixtures.RepoRoot(), "..", "gadak"),
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "cmd", "gadak")); err == nil && st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	if os.Getenv("ISSUETAP_REQUIRE_GADAK") == "1" {
		t.Fatal("gadak source not found; set ISSUETAP_GADAK_SRC")
	}
	t.Skip("gadak source not found; set ISSUETAP_GADAK_SRC to run conformance")
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// internal/conformance → repo root
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func buildGadak(t *testing.T) string {
	t.Helper()
	src := gadakSrc(t)
	out := filepath.Join(t.TempDir(), "gadak-it")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/gadak")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build gadak: %v\n%s", err, b)
	}
	return out
}

func startIssuetap(t *testing.T, fixture string, loc locale.Code, fs []faults.Fault) (base string, st *store.Store) {
	t.Helper()
	doc, err := fixtures.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	st = store.New(store.Options{Seed: 1, Locale: loc})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	s := api.New(cfg, st, faults.New(fs), nil, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String(), st
}

func writeGadakHome(t *testing.T, site string) string {
	t.Helper()
	home := t.TempDir()
	off := false
	cfg := map[string]any{
		"site":        site,
		"email":       "you@example.com",
		"token":       "issuetap",
		"updateCheck": off,
		"notify":      off,
		"confluence":  map[string]any{"spaces": []string{"DOCS"}},
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestGadakSyncMirror(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, nil)
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(),
		"GADAK_HOME="+home,
		"GADAK_PROFILE=",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync:\n%s", out)
	if err != nil {
		t.Fatalf("gadak sync: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(home, "gadak.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var issues, comments, hist, pages int
	if err := db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM changelog`).Scan(&hist); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pages`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if issues != 3 {
		t.Errorf("issues=%d want 3", issues)
	}
	if comments < 2 {
		t.Errorf("comments=%d want >=2", comments)
	}
	if hist < 3 {
		t.Errorf("changelog=%d want >=3", hist)
	}
	if pages != 1 {
		t.Errorf("pages=%d want 1", pages)
	}

	var keys string
	rows, err := db.Query(`SELECT key FROM issues ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		got = append(got, k)
	}
	keys = strings.Join(got, ",")
	if keys != "TAP-1,TAP-2,TAP-3" {
		t.Errorf("keys=%s", keys)
	}

	var status, statusID string
	if err := db.QueryRow(`SELECT status, status_id FROM issues WHERE key='TAP-1'`).Scan(&status, &statusID); err != nil {
		t.Fatal(err)
	}
	if statusID != "3" {
		t.Errorf("TAP-1 status_id=%s want 3", statusID)
	}
	if status == "" {
		t.Error("TAP-1 status name empty")
	}
}

func TestGadakStopsOnRevokedCredential(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, []faults.Fault{{
		Name: "revoke-mid-sync", After: 2, Status: 401, PathPrefix: "/rest/",
	}})
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync (revoked):\n%s", out)
	if err == nil {
		t.Fatal("expected gadak sync to fail after 401")
	}
	if !strings.Contains(string(out), "credential rejected") && !strings.Contains(err.Error(), "exit") {
		// gadak prints the error; accept any non-zero.
		t.Logf("err=%v", err)
	}
}

func TestGadakBackoffOn429(t *testing.T) {
	bin := buildGadak(t)
	root := repoRoot(t)
	base, _ := startIssuetap(t, filepath.Join(root, "examples/fixtures/tiny.yaml"), locale.EN, []faults.Fault{{
		Name: "one-429", Times: 1, Status: 429, RetryAfter: 1, PathContains: "/search/jql",
	}})
	home := writeGadakHome(t, base)

	cmd := exec.Command(bin, "sync", "--full")
	cmd.Env = append(os.Environ(), "GADAK_HOME="+home)
	out, err := cmd.CombinedOutput()
	t.Logf("gadak sync (429):\n%s", out)
	if err != nil {
		t.Fatalf("gadak should succeed after retrying 429: %v", err)
	}
}
