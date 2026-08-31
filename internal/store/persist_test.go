package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

// TestMutationSurvivesRestart is the persistence contract: a mutation made
// through the store, with no manual snapshot, is still there when a new
// store opens the same persistence file. FAIL-first 2026-08-17: before
// write-through persistence the restarted store came back fixture-only
// (scratch/failfirst-persist-demo.txt: total 3 → 2, DATA LOST).
func TestMutationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment("TAP-1", "", []byte("must survive a restart")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	iss := st2.Issue("TAP-1")
	if iss == nil {
		t.Fatal("TAP-1 lost across restart")
	}
	found := false
	for _, c := range iss.Comments {
		if c.BodyText == "must survive a restart" {
			found = true
		}
	}
	if !found {
		t.Fatalf("comment lost across restart; comments=%d", len(iss.Comments))
	}
}

// TestDebouncedAutoWrite: mutations reach disk without Flush/Close.
// PersistDebounce is a no-op; ACK is the write.
func TestDebouncedAutoWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: 20 * time.Millisecond})
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
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "debounced",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got := st2.Issue(iss.Key)
	if got == nil || got.Summary != "debounced" {
		t.Fatalf("ACK'd issue missing on reopen without Close: %+v", got)
	}
}

// TestCloseFlushesDirtyState: a mutation immediately before Close (inside
// the debounce window) must still hit the file.
func TestCloseFlushesDirtyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAssignee("TAP-1", "5b10a2844c20165700ede22g", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if iss := st2.Issue("TAP-1"); iss == nil || iss.AssigneeID != "5b10a2844c20165700ede22g" {
		t.Fatalf("assignee not restored: %+v", st2.Issue("TAP-1"))
	}
}

// TestPersistedFileIsAtomic: SQLite/WAL commits are the persist path.
// Observable proxy: after a burst of writes the export Snapshot is a
// parseable YAML fixture and a fresh Open serves the graph.
func TestPersistedFileIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := st.AddComment("TAP-1", "", []byte("burst "+strings.Repeat("x", i))); err != nil {
			t.Fatal(err)
		}
	}
	b, err := fixtures.MarshalYAML(st.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.Parse(b, ".yaml"); err != nil {
		t.Fatalf("Snapshot is not a parseable fixture: %v", err)
	}
	st.Close()
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n := len(st2.Issue("TAP-1").Comments); n < 20 {
		t.Fatalf("reopened TAP-1 comments=%d, want ≥20", n)
	}
}

// TestRestartMutationsAdvanceClock: after loading a persisted state, new
// mutations must be stamped later than everything in the document, or an
// `updated >=` delta sync (gadak) re-reading after a restart would skip
// them.
func TestRestartMutationsAdvanceClock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment("TAP-1", "", []byte("first run")); err != nil {
		t.Fatal(err)
	}
	pre := st.Issue("TAP-1").Updated
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if _, err := st2.AddComment("TAP-1", "", []byte("after restart")); err != nil {
		t.Fatal(err)
	}
	post := st2.Issue("TAP-1").Updated
	if post <= pre {
		t.Fatalf("post-restart mutation stamp %s not after pre-restart %s (delta sync would skip it)", post, pre)
	}
}

// TestRestartDoesNotReuseIds: comment/attachment/history ids continue
// after the highest restored id instead of restarting at 1 and colliding.
func TestRestartDoesNotReuseIds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	first, err := st.AddComment("TAP-1", "", []byte("first run"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	restored := len(st2.Issue("TAP-1").Comments)
	if want := len(st.Issue("TAP-1").Comments); restored != want {
		t.Fatalf("comment count changed across restart: %d vs %d", restored, want)
	}
	second, err := st2.AddComment("TAP-1", "", []byte("after restart"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID <= first.ID {
		t.Fatalf("post-restart comment id %s collides with restored %s", second.ID, first.ID)
	}
}

// TestPersistCorruptFileFailsLoud: opening a corrupt persistence file is
// an error, never a silent empty store (honesty over recovery).
func TestPersistCorruptFileFailsLoud(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.WriteFile(path, []byte("issues: [ {key: BROKEN"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{PersistPath: path}); err == nil {
		t.Fatal("expected an error opening a corrupt persistence file")
	}
}

// persistPathAsDir turns path into a directory. YAML write-through used
// this to fail a rename; the live SQLite connection keeps the fd so a
// later mutation still commits.
func persistPathAsDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func loadTinyDoc(t *testing.T) fixtures.Doc {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestFlushWritesLatestAndSurfacesError: Flush is a WAL checkpoint.
// Mutations are already on disk before Flush; a sibling Open sees them.
// Replacing the path with a directory after Open no longer fails the
// live connection (the fd is held) — Open of a directory is the error.
func TestFlushWritesLatestAndSurfacesError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "flush-latest",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatalf("Flush did not leave a readable DB: %v", err)
	}
	defer st2.Close()
	got := st2.Issue(iss.Key)
	if got == nil || got.Summary != "flush-latest" {
		t.Fatalf("flushed DB missing latest mutation: %+v", got)
	}
	if err := st.PersistErr(); err != nil {
		t.Fatalf("PersistErr after successful Flush: %v", err)
	}

	dirPath := filepath.Join(t.TempDir(), "not-a-db")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{PersistPath: dirPath}); err == nil {
		t.Fatal("expected Open of a directory PersistPath to fail")
	}
}

// TestPersistRetriesAfterFailureWithoutMutation: YAML write-through
// retried a failed rename. Stage 3 has no debounce retry — ACK is the
// SQL commit — so a sibling Open without further mutation sees the write.
func TestPersistRetriesAfterFailureWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment("TAP-1", "", []byte("retry-after-fail")); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	found := false
	for _, c := range st2.Issue("TAP-1").Comments {
		if c.BodyText == "retry-after-fail" {
			found = true
		}
	}
	if !found {
		t.Fatal("ACK'd comment missing on sibling Open; debounce retry is gone")
	}
}

// TestNegativeDebounceWritesBeforeReturn: PersistDebounce < 0 writes on
// the mutation call, so the file is current without Flush or waiting.
func TestNegativeDebounceWritesBeforeReturn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "sync-now",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatalf("negative debounce did not write before return: %v", err)
	}
	defer st2.Close()
	got := st2.Issue(iss.Key)
	if got == nil || got.Summary != "sync-now" {
		t.Fatalf("sync persist missing mutation: %+v", got)
	}
}

// TestDurablePersistFailureReturnsFromMutation: YAML write-through could
// fail a rename after the graph mutation (PersistError, in-memory kept).
// Stage 3's working copy is the file: a directory PersistPath fails at
// Open, and a live handle's SQL commit is the ACK (no split-brain).
func TestDurablePersistFailureReturnsFromMutation(t *testing.T) {
	dirPath := filepath.Join(t.TempDir(), "state.yaml")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: dirPath, PersistDebounce: -1}); err == nil {
		t.Fatal("expected Open of a directory PersistPath to fail")
	}

	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "durable-ok",
	}, "")
	if err != nil {
		t.Fatalf("file-backed CreateIssue: %v", err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got := st2.Issue(iss.Key); got == nil || got.Summary != "durable-ok" {
		t.Fatalf("ACK'd issue missing on reopen: %+v", got)
	}
}

// TestDurablePersistFailureAllMutations is the markDirtyLocked census:
// every mutation commits before return so a sibling Open without Close
// sees it. (YAML-era: each returned PersistError when the rename failed.)
func TestDurablePersistFailureAllMutations(t *testing.T) {
	adf := []byte(`{"type":"doc","version":1,"content":[]}`)
	cases := []struct {
		name string
		fn   func(*Store) error
		see  func(*testing.T, *Store)
	}{
		{"CreateIssue", func(st *Store) error {
			_, err := st.CreateIssue(map[string]any{
				"project": map[string]any{"key": "TAP"}, "summary": "census",
			}, "")
			return err
		}, func(t *testing.T, st2 *Store) {
			found := false
			for _, iss := range st2.Snapshot().Issues {
				if iss.Summary == "census" {
					found = true
				}
			}
			if !found {
				t.Fatal("CreateIssue not visible on sibling Open")
			}
		}},
		{"UpdateIssue", func(st *Store) error {
			return st.UpdateIssue("TAP-1", map[string]any{"summary": "census"}, nil, "")
		}, func(t *testing.T, st2 *Store) {
			if iss := st2.Issue("TAP-1"); iss == nil || iss.Summary != "census" {
				t.Fatalf("UpdateIssue not visible: %+v", iss)
			}
		}},
		{"AddComment", func(st *Store) error {
			_, err := st.AddComment("TAP-1", "", []byte("census"))
			return err
		}, func(t *testing.T, st2 *Store) {
			iss := st2.Issue("TAP-1")
			if iss == nil {
				t.Fatal("TAP-1 missing")
			}
			ok := false
			for _, c := range iss.Comments {
				if c.BodyText == "census" {
					ok = true
				}
			}
			if !ok {
				t.Fatal("AddComment not visible on sibling Open")
			}
		}},
		{"SetAssignee", func(st *Store) error {
			return st.SetAssignee("TAP-1", "5b10a2844c20165700ede21g", "")
		}, func(t *testing.T, st2 *Store) {
			if iss := st2.Issue("TAP-1"); iss == nil || iss.AssigneeID != "5b10a2844c20165700ede21g" {
				t.Fatalf("SetAssignee not visible: %+v", st2.Issue("TAP-1"))
			}
		}},
		{"Transition", func(st *Store) error {
			return st.Transition("TAP-1", "1")
		}, func(t *testing.T, st2 *Store) {
			if st2.Issue("TAP-1") == nil {
				t.Fatal("TAP-1 missing after Transition")
			}
		}},
		{"AddAttachment", func(st *Store) error {
			_, err := st.AddAttachment("TAP-1", "a.txt", "text/plain", "", []byte("hi"))
			return err
		}, func(t *testing.T, st2 *Store) {
			if st2.Issue("TAP-1") == nil || len(st2.Issue("TAP-1").Attachments) == 0 {
				t.Fatal("AddAttachment not visible on sibling Open")
			}
		}},
		{"CreatePage", func(st *Store) error {
			_, err := st.CreatePage(PageWrite{Title: "census", SpaceKey: "DOCS", BodyADF: adf})
			return err
		}, func(t *testing.T, st2 *Store) {
			found := false
			for _, p := range st2.Snapshot().Pages {
				if p.Title == "census" {
					found = true
				}
			}
			if !found {
				t.Fatal("CreatePage not visible on sibling Open")
			}
		}},
		{"UpdatePage", func(st *Store) error {
			_, err := st.UpdatePage("20001", PageWrite{Title: "Welcome to the lab", Next: 2, BodyADF: adf})
			return err
		}, func(t *testing.T, st2 *Store) {
			if st2.Page("20001") == nil {
				t.Fatal("page 20001 missing after UpdatePage")
			}
		}},
		{"Apply", func(st *Store) error {
			return st.Apply(loadTinyDoc(t))
		}, func(t *testing.T, st2 *Store) {
			if st2.Issue("TAP-1") == nil {
				t.Fatal("Apply not visible on sibling Open")
			}
		}},
		{"SetLocale", func(st *Store) error {
			return st.SetLocale(locale.KO)
		}, func(t *testing.T, st2 *Store) {
			if st2.Locale() != locale.KO {
				t.Fatalf("SetLocale not visible: %s", st2.Locale())
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.yaml")
			st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.Apply(loadTinyDoc(t)); err != nil {
				t.Fatal(err)
			}
			if err := tc.fn(st); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st2.Close()
			tc.see(t, st2)
		})
	}
}

// TestDebouncedPersistFailureDoesNotFailMutation: YAML debounce returned
// nil from the mutation while persist retried. Stage 3 commits SQL
// before return, so the mutation still succeeds even if the path name
// is replaced after Open (the live connection holds the fd).
func TestDebouncedPersistFailureDoesNotFailMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}

	persistPathAsDir(t, path)
	if _, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "debounce-fail",
	}, ""); err != nil {
		t.Fatalf("debounced mutation must succeed while persist retries: %v", err)
	}
}
