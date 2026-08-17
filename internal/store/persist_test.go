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

// waitForFile polls until the persistence layer has written the file.
func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			return b
		}
		if time.Now().After(deadline) {
			t.Fatalf("persistence file %s not written within 2s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

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

// TestDebouncedAutoWrite: mutations reach disk without Flush/Close — the
// debounce timer does the writing, so a hard kill loses at most one
// window.
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
	if _, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"}, "summary": "debounced",
	}); err != nil {
		t.Fatal(err)
	}
	b := waitForFile(t, path)
	if !strings.Contains(string(b), "debounced") {
		t.Fatalf("persisted file does not contain the created issue:\n%s", b)
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
	if err := st.SetAssignee("TAP-1", "5b10a2844c20165700ede22g"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Close did not flush: %v", err)
	}
	if !strings.Contains(string(b), "TAP-1") {
		t.Fatal("flushed file missing TAP-1")
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

// TestPersistedFileIsAtomic: the write path is temp-file + rename, so a
// reader never observes a partial document. Observable proxy: every write
// replaces the file with a fully parseable document. (The atomicity
// itself is structural — same-directory rename.)
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
	st.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.Parse(b, ".yaml"); err != nil {
		t.Fatalf("persisted file is not a parseable fixture: %v", err)
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
