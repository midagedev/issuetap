// Command issuetap is the single binary: serve, fixtures, scenario, diagnose.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/midagedev/issuetap"
	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/diagnostics"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/scenarios"
	"github.com/midagedev/issuetap/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "fixtures":
		err = cmdFixtures(os.Args[2:])
	case "scenario":
		err = cmdScenario(os.Args[2:])
	case "diagnose":
		err = cmdDiagnose(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("issuetap 0.1.0")
		return
	case "help", "-h", "--help":
		usage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "issuetap: unknown command %q\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "issuetap: %v\n", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `issuetap — local Atlassian testbed

Usage:
  issuetap serve [flags]
  issuetap fixtures apply <file>
  issuetap fixtures snapshot [--addr host:port] [--format yaml|json]
  issuetap scenario run <file> [--addr host:port] [--report path]
  issuetap diagnose [--addr host:port] [--out file.zip]
  issuetap version

Flags for serve:
  --addr          listen address (default 127.0.0.1:8080)
  --fixture       YAML/JSON fixture to load
  --locale        en | ko | ja | de
  --dialect       cloud | dc
  --context-path  DC context path, e.g. /jira
  --seed          determinism seed (default 1)
  --email         accepted Basic user (empty = any)
  --token         accepted Basic password / Bearer PAT (empty = any non-empty)
  --scenario      scenario file to apply (faults + locale) on start
  --persist       on-disk SQLite state file (recommended .db). Mutations
                  commit before the HTTP response returns, and the file is
                  the working copy on restart. When the file exists it
                  supersedes --fixture and the scenario fixture; delete it
                  to reseed from the fixture. A legacy YAML persist file
                  is refused — pass it as --fixture and set --persist to a
                  new .db.
  --persist-debounce  retained for flag compatibility; no-op (writes
                  always commit before the response returns).
`)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfg := config.FromEnv(config.Default())
	addr := fs.String("addr", cfg.Addr, "listen address")
	fixture := fs.String("fixture", cfg.Fixture, "fixture file")
	loc := fs.String("locale", string(cfg.Locale), "locale")
	dial := fs.String("dialect", string(cfg.Dialect.Kind), "cloud or dc")
	ctxPath := fs.String("context-path", cfg.Dialect.ContextPath, "DC context path")
	seed := fs.Int64("seed", cfg.Seed, "determinism seed")
	email := fs.String("email", cfg.Email, "accepted Basic user")
	token := fs.String("token", cfg.Token, "accepted token")
	scenario := fs.String("scenario", "", "scenario file to apply on start")
	persist := fs.String("persist", cfg.Snapshot, "on-disk SQLite persistence file (ISSUETAP_SNAPSHOT)")
	persistDebounce := fs.Duration("persist-debounce", 0, "retained; no-op (writes always commit before the HTTP response returns)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Addr = *addr
	cfg.Fixture = *fixture
	cfg.Locale = locale.Parse(*loc)
	cfg.Dialect = dialect.Config{Kind: dialect.Parse(*dial), ContextPath: dialect.NormalizeContext(*ctxPath)}
	cfg.Seed = *seed
	cfg.Email = *email
	cfg.Token = *token

	localeFromCLI := os.Getenv("ISSUETAP_LOCALE") != ""
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "locale" {
			localeFromCLI = true
		}
	})

	st, eng, err := loadServeGraph(cfg, *scenario, localeFromCLI, *persist, *persistDebounce)
	if err != nil {
		return err
	}

	ui, ok := issuetap.WebUI()
	srvAPI := api.New(cfg, st, eng, ui, ok)
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srvAPI.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sh)
	}()

	fmt.Fprintf(os.Stderr, "issuetap listening on http://%s  dialect=%s locale=%s seed=%d fixture=%s persist=%s\n",
		cfg.Addr, cfg.Dialect.Kind, st.Locale(), st.Seed(), cfg.Fixture, persistStatus(*persist))
	err = httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		err = nil
	}
	// Checkpoint on-disk persistence after the listener is down.
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	return err
}

// loadServeGraph is the serve bootstrap: Open persist (if any) → Apply
// fixture → optional scenario (faults + scenario locale). With persist
// set and an existing SQLite state file, that file is the graph (it is
// by definition a later state of the same fixture) and the scenario
// fixture is skipped; scenario faults and locale still apply. When
// localeFromCLI is true (--locale or ISSUETAP_LOCALE), that locale is
// applied last so it wins over the fixture locale: field.
func loadServeGraph(cfg config.Config, scenarioPath string, localeFromCLI bool, persist string, persistDebounce time.Duration) (*store.Store, *faults.Engine, error) {
	loadedPersist := false
	if persist != "" {
		if _, err := os.Stat(persist); err == nil {
			loadedPersist = true
		} else if !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("--persist %s: %w", persist, err)
		}
	}
	st, err := store.Open(store.Options{
		Seed: cfg.Seed, Locale: cfg.Locale,
		PersistPath: persist, PersistDebounce: persistDebounce,
	})
	if err != nil {
		return nil, nil, err
	}
	if !loadedPersist && cfg.Fixture != "" {
		doc, err := fixtures.Load(cfg.Fixture)
		if err != nil {
			return nil, nil, err
		}
		if err := st.Apply(doc); err != nil {
			return nil, nil, err
		}
	}
	eng := faults.New(nil)
	if scenarioPath != "" {
		sc, err := scenarios.Load(scenarioPath)
		if err != nil {
			return nil, nil, err
		}
		if sc.Fixture != "" && cfg.Fixture == "" && !loadedPersist {
			doc, err := fixtures.Load(sc.Fixture)
			if err != nil {
				return nil, nil, err
			}
			if err := st.Apply(doc); err != nil {
				return nil, nil, err
			}
		}
		if sc.Locale != "" {
			st.SetLocale(locale.Parse(sc.Locale))
		}
		eng.Replace(sc.Faults)
	}
	if localeFromCLI {
		st.SetLocale(cfg.Locale)
	}
	return st, eng, nil
}

func cmdFixtures(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: issuetap fixtures apply <file> | snapshot")
	}
	switch args[0] {
	case "apply":
		if len(args) < 2 {
			return fmt.Errorf("usage: issuetap fixtures apply <file>")
		}
		doc, err := fixtures.Load(args[1])
		if err != nil {
			return err
		}
		st := store.New(store.Options{Seed: doc.Seed, Locale: locale.Parse(doc.Locale)})
		if err := st.Apply(doc); err != nil {
			return err
		}
		c := st.Counts()
		fmt.Printf("ok %s  issues=%d pages=%d projects=%d spaces=%d (offline validate; POST /api/fixtures/apply to a running server)\n",
			args[1], c["issues"], c["pages"], c["projects"], c["spaces"])
		return nil
	case "snapshot":
		return cmdFixturesSnapshot(args[1:])
	default:
		return fmt.Errorf("unknown fixtures command %q", args[0])
	}
}

func cmdFixturesSnapshot(args []string) error {
	fs := flag.NewFlagSet("fixtures snapshot", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "running server")
	format := fs.String("format", "yaml", "yaml or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "yaml" && *format != "json" {
		return fmt.Errorf("usage: issuetap fixtures snapshot [--addr host:port] [--format yaml|json]")
	}
	base := *addr
	if !hasScheme(base) {
		base = "http://" + base
	}
	url := base + "/api/fixtures/snapshot"
	if *format == "json" {
		url += "?format=json"
	}
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("could not reach %s (%v); start issuetap serve first", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, clipBytes(b, 256))
	}
	_, err = os.Stdout.Write(b)
	return err
}

func clipBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func cmdScenario(args []string) error {
	if len(args) < 1 || args[0] != "run" || len(args) < 2 {
		return fmt.Errorf("usage: issuetap scenario run <file> [--addr host:port] [--report path]")
	}
	fs := flag.NewFlagSet("scenario", flag.ContinueOnError)
	addr := fs.String("addr", "", "existing server; empty starts an ephemeral one")
	report := fs.String("report", "", "write JSON report here")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	sc, err := scenarios.Load(args[1])
	if err != nil {
		return err
	}

	base := *addr
	var shutdown func()
	if base == "" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		cfg := config.Default()
		cfg.Addr = ln.Addr().String()
		if sc.Dialect != "" {
			cfg.Dialect.Kind = dialect.Parse(sc.Dialect)
		}
		if sc.ContextPath != "" {
			cfg.Dialect.ContextPath = dialect.NormalizeContext(sc.ContextPath)
		}
		st := store.New(store.Options{Seed: sc.Seed, Locale: locale.Parse(sc.Locale)})
		if sc.Fixture != "" {
			doc, err := fixtures.Load(sc.Fixture)
			if err != nil {
				return err
			}
			if err := st.Apply(doc); err != nil {
				return err
			}
		}
		// Scenario locale/dialect win over the fixture document.
		if sc.Locale != "" {
			st.SetLocale(locale.Parse(sc.Locale))
		}
		eng := faults.New(sc.Faults)
		ui, ok := issuetap.WebUI()
		apiSrv := api.New(cfg, st, eng, ui, ok)
		hs := &http.Server{Handler: apiSrv.Handler(), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = hs.Serve(ln) }()
		base = "http://" + ln.Addr().String()
		shutdown = func() { _ = hs.Close() }
		defer shutdown()
	} else if !hasScheme(base) {
		base = "http://" + base
	}

	rep := scenarios.RunAssertions(base, sc)
	if *report != "" {
		if err := os.WriteFile(*report, rep.Marshal(), 0o644); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
	if !rep.Passed {
		return fmt.Errorf("scenario %s: %d assertion(s) failed", sc.Name, rep.FailCount())
	}
	return nil
}

func cmdDiagnose(args []string) error {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "running server")
	out := fs.String("out", "issuetap-diagnose.zip", "output zip")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := *addr
	if !hasScheme(base) {
		base = "http://" + base
	}
	resp, err := http.Get(base + "/api/diagnostics")
	if err != nil {
		// Offline fallback: empty bundle with a note.
		b, err := diagnostics.Build(diagnostics.Input{
			Dialect: "unknown",
			Counts:  map[string]int{},
		})
		if err != nil {
			return err
		}
		if werr := os.WriteFile(*out, b, 0o644); werr != nil {
			return werr
		}
		return fmt.Errorf("could not reach %s (%v); wrote an empty bundle to %s", base, err, *out)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", *out, len(b))
	return nil
}

func hasScheme(s string) bool {
	return len(s) > 7 && (s[:7] == "http://" || (len(s) > 8 && s[:8] == "https://"))
}

// persistStatus describes the --persist state for the startup line: off,
// armed (fresh file), or the path being continued.
func persistStatus(persist string) string {
	if persist == "" {
		return "off"
	}
	if _, err := os.Stat(persist); err == nil {
		return persist + " (continuing)"
	}
	return persist + " (new)"
}
