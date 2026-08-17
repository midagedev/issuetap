package issuetap

// The public embedding surface lives in this file. It wraps the internal
// api/store constructors so an external Go program can run issuetap
// in-process without importing anything under internal/.

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/store"
)

// EmbeddedConfig is everything an embedding program can set. Every field
// is optional; zero values give the same defaults as `issuetap serve`.
//
// Load order at startup: when PersistPath names an existing file, that
// file is the initial graph (it is a later state of whatever fixture the
// run started from) and the fixture fields are skipped. Otherwise
// FixturePath, then FixtureBytes, seeds the graph. Locale, when non-empty,
// wins over a fixture's `locale:` field.
type EmbeddedConfig struct {
	Seed            int64         // determinism seed (0 → 1)
	Locale          string        // "" | en | ko | ja | de
	Dialect         string        // "" | cloud | dc
	ContextPath     string        // DC context path, e.g. /jira
	Email           string        // accepted Basic user ("" = any)
	Token           string        // accepted secret ("" = any non-empty)
	FixturePath     string        // YAML/JSON fixture file to seed from
	FixtureBytes    []byte        // fixture contents when FixturePath is empty
	PersistPath     string        // write-through state file (see store.Options)
	PersistDebounce time.Duration // quiet window before a write (0 → 1s)
}

// Embedded is issuetap as an in-process dependency: the public embedding
// contract (임베딩용 공개 계약). It serves the full HTTP surface — Jira
// Cloud v3 under /rest/api/3, Confluence under /wiki, the dashboard API
// under /api, and the embedded web UI — behind one http.Handler. No
// internal type appears in any signature, so this package is the only
// import an embedding program needs.
type Embedded struct {
	st *store.Store
	h  http.Handler
}

// NewEmbedded builds an in-process issuetap. Configuration errors (an
// unreadable fixture, a corrupt persistence file) are returned, never
// swallowed into an empty server.
func NewEmbedded(cfg EmbeddedConfig) (*Embedded, error) {
	seed := cfg.Seed
	if seed == 0 {
		seed = 1
	}
	loc := locale.Parse(cfg.Locale)
	ccfg := config.Config{
		Locale:  loc,
		Dialect: dialect.Config{Kind: dialect.Parse(cfg.Dialect), ContextPath: dialect.NormalizeContext(cfg.ContextPath)},
		Seed:    seed,
		Email:   cfg.Email,
		Token:   cfg.Token,
	}
	st, err := store.Open(store.Options{
		Seed: seed, Locale: loc,
		PersistPath: cfg.PersistPath, PersistDebounce: cfg.PersistDebounce,
	})
	if err != nil {
		return nil, err
	}
	persistLoaded := false
	if cfg.PersistPath != "" {
		if _, err := os.Stat(cfg.PersistPath); err == nil {
			persistLoaded = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("persist %s: %w", cfg.PersistPath, err)
		}
	}
	if !persistLoaded {
		var doc fixtures.Doc
		seeded := false
		switch {
		case cfg.FixturePath != "":
			doc, err = fixtures.Load(cfg.FixturePath)
			seeded = true
		case len(cfg.FixtureBytes) > 0:
			doc, err = fixtures.Parse(cfg.FixtureBytes, "")
			seeded = true
		}
		if err != nil {
			return nil, err
		}
		if seeded {
			if err := st.Apply(doc); err != nil {
				return nil, err
			}
		}
	}
	if cfg.Locale != "" { // explicit locale beats the fixture's locale: field
		st.SetLocale(loc)
	}
	ui, uiOK := WebUI()
	srv := api.New(ccfg, st, nil, ui, uiOK)
	return &Embedded{st: st, h: srv.Handler()}, nil
}

// ServeHTTP serves the whole issuetap surface. Mount it on its own
// httptest server, mux, or listener.
func (e *Embedded) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.h.ServeHTTP(w, r)
}

// LoadFixture replaces the graph with a YAML/JSON fixture file.
func (e *Embedded) LoadFixture(path string) error {
	doc, err := fixtures.Load(path)
	if err != nil {
		return err
	}
	return e.st.Apply(doc)
}

// LoadFixtureBytes replaces the graph with an in-memory YAML/JSON
// fixture document.
func (e *Embedded) LoadFixtureBytes(b []byte) error {
	doc, err := fixtures.Parse(b, "")
	if err != nil {
		return err
	}
	return e.st.Apply(doc)
}

// Snapshot returns the current graph as a YAML fixture document — the
// same bytes `issuetap fixtures snapshot` produces. Attachment content is
// included (inline text or base64) so the document round-trips.
func (e *Embedded) Snapshot() ([]byte, error) {
	return fixtures.MarshalYAML(e.st.Snapshot())
}

// Close flushes write-through persistence (when armed) and stops the
// debounce timer. Always call before dropping the Embedded value.
func (e *Embedded) Close() error {
	return e.st.Close()
}
