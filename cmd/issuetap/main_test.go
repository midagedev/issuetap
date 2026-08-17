package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func TestServeLocaleFlagWinsFixture(t *testing.T) {
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	cfg.Locale = locale.KO
	st, _, err := loadServeGraph(cfg, "", true, "")
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
	st, _, err := loadServeGraph(cfg, "", false, "")
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
	loaded, _, err := loadServeGraph(cfg, "", false, path)
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
