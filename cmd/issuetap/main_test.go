package main

import (
	"testing"

	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func TestServeLocaleFlagWinsFixture(t *testing.T) {
	cfg := config.Default()
	cfg.Fixture = fixtures.Example("tiny.yaml")
	cfg.Locale = locale.KO
	st, _, err := loadServeGraph(cfg, "", true)
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
	st, _, err := loadServeGraph(cfg, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Locale() != locale.EN {
		t.Fatalf("fixture locale:en should remain when --locale is omitted, got %s", st.Locale())
	}
}
