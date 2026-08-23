package store

// Contract ↔ assertion mapping (F′ stage 3 / gadak GDK-202):
//
// | Clause | Happy path | Violation / boundary |
// | --- | --- | --- |
// | Durable-before-return: ACK'd write is visible on a new Open of the same PersistPath without Close | TestDurableWriteVisibleWithoutClose | TestDebounceWindowNoLongerDropsWrite (1s debounce used to lose it) |
// | Write cost constant vs corpus: 5_000 / 500 comment-write median ≤ 2× | TestCommentWriteCostConstantVsCorpus | same test fails the ≥5× YAML class |
// | Two Open handles on one DB file coexist without corruption | TestDualOpenSameDBConsistentReread | TestDualOpenSameDBInterleavedWritesNoCorrupt |
// | PersistPath pointing at YAML is refused with a FixturePath migration hint | TestPersistPathYAMLRejected | TestPersistPathYAMLNotSilentlyOverwritten |
// | Corrupt DB file fails loud | TestPersistCorruptDBFailsLoud | TestPersistTruncatedSQLiteFailsLoud / TestPersistEmptyFileRejected |
// | schema_version mismatch is refused | TestPersistSchemaTooOldRejected | TestPersistSchemaTooNewRejected |
// | Snapshot() still returns a YAML fixture document from DB state | TestSnapshotYAMLFromDBState | TestSnapshotYAMLRoundTripAfterPersistWrite |
//
// PersistDebounce is retained as a field and is a no-op.
//
// Self-review defect classes not asserted here (see report):
// 1. Two writers mutating the same issue blob (last-write-wins) — this
//    round does not support multi-writer; dual-open uses distinct keys.
// 2. Crash between createFileDB and the caller's Apply — next Open sees
//    an empty DB and embedders skip the fixture. Stage 1 can delay file
//    creation until first mutation.
// 3. Close leaves the *sql.DB open so post-Close getters still work
//    (TestRestartDoesNotReuseIds). WAL sidecars stay until process exit.

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"

	_ "modernc.org/sqlite"
)

func persistDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.db")
}

func applyTiny(t *testing.T, st *Store) {
	t.Helper()
	if err := st.Apply(loadTinyDoc(t)); err != nil {
		t.Fatal(err)
	}
}

func commentBodies(iss *modelIssueComments) []string {
	if iss == nil {
		return nil
	}
	out := make([]string, 0, len(iss.comments))
	out = append(out, iss.comments...)
	return out
}

// modelIssueComments is a tiny projection so tests don't range the full Issue
// in every helper signature.
type modelIssueComments struct {
	comments []string
}

func issueCommentTexts(st *Store, key string) *modelIssueComments {
	iss := st.Issue(key)
	if iss == nil {
		return nil
	}
	out := &modelIssueComments{}
	for _, c := range iss.Comments {
		out.comments = append(out.comments, c.BodyText)
	}
	return out
}

func hasComment(st *Store, key, body string) bool {
	got := issueCommentTexts(st, key)
	if got == nil {
		return false
	}
	for _, c := range got.comments {
		if c == body {
			return true
		}
	}
	return false
}

// TestDurableWriteVisibleWithoutClose: a write API that has returned (ACK)
// is visible on a fresh Open of the same PersistPath without Close of the
// first handle. FAIL-first 2026-08-24: YAML PersistDebounce=1s lost the
// write (see TestDebounceWindowNoLongerDropsWrite).
func TestDurableWriteVisibleWithoutClose(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	applyTiny(t, st)
	const body = "durable-without-close"
	if _, err := st.AddComment("TAP-1", "", []byte(body)); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if !hasComment(st2, "TAP-1", body) {
		t.Fatalf("ACK'd comment %q missing on reopen without Close; comments=%v", body, commentBodies(issueCommentTexts(st2, "TAP-1")))
	}
}

// TestDebounceWindowNoLongerDropsWrite is the boundary twin: PersistDebounce
// is a no-op, so even a 1-hour value cannot hide an ACK'd write from a
// sibling Open.
func TestDebounceWindowNoLongerDropsWrite(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	applyTiny(t, st)
	const body = "debounce-noop"
	if _, err := st.AddComment("TAP-1", "", []byte(body)); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if !hasComment(st2, "TAP-1", body) {
		t.Fatal("PersistDebounce is a no-op; ACK'd write must be on disk before return")
	}
}

func corpusDoc(n int) fixtures.Doc {
	doc := fixtures.Doc{Seed: 1, Locale: "en"}
	doc.Projects = []fixtures.Project{{Key: "TAP", Name: "Tap"}}
	doc.Users = []fixtures.User{{AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada Lovelace"}}
	doc.Issues = make([]fixtures.Issue, 0, n)
	for i := 1; i <= n; i++ {
		doc.Issues = append(doc.Issues, fixtures.Issue{
			Key:     fmt.Sprintf("TAP-%d", i),
			Summary: fmt.Sprintf("corpus issue %d %s", i, strings.Repeat("x", 32)),
		})
	}
	return doc
}

func medianCommentPersist(t *testing.T, n, samples int) time.Duration {
	t.Helper()
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Apply(corpusDoc(n)); err != nil {
		t.Fatal(err)
	}
	durations := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		if _, err := st.AddComment("TAP-1", "", []byte(fmt.Sprintf("c-%d", i))); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[len(durations)/2]
}

// TestCommentWriteCostConstantVsCorpus: a single persisted comment write
// against 5_000 issues must not exceed 2× the median of the same write
// against 500. FAIL-first 2026-08-24: YAML write-through scaled with the
// document (≥5×).
func TestCommentWriteCostConstantVsCorpus(t *testing.T) {
	const samples = 7
	med500 := medianCommentPersist(t, 500, samples)
	med5000 := medianCommentPersist(t, 5000, samples)
	if med500 <= 0 {
		t.Fatalf("500-issue median was %s", med500)
	}
	ratio := float64(med5000) / float64(med500)
	t.Logf("comment-write median 500=%s 5000=%s ratio=%.2f", med500, med5000, ratio)
	if ratio > 2.0 {
		t.Fatalf("comment-write median ratio 5000/500 = %.2f (500=%s, 5000=%s); want ≤ 2× (YAML class was ≥5×)", ratio, med500, med5000)
	}
}

// TestDualOpenSameDBConsistentReread: two Open handles on one file, writes
// to distinct issues, both re-read the committed rows. This is a WAL
// premise check, not multi-writer support.
func TestDualOpenSameDBConsistentReread(t *testing.T) {
	path := persistDBPath(t)
	a, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	applyTiny(t, a)

	b, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if _, err := a.AddComment("TAP-1", "", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddComment("TAP-2", "", []byte("from-b")); err != nil {
		t.Fatal(err)
	}

	if !hasComment(a, "TAP-1", "from-a") {
		t.Fatal("writer a lost its own TAP-1 comment")
	}
	if !hasComment(b, "TAP-2", "from-b") {
		t.Fatal("writer b lost its own TAP-2 comment")
	}
	if !hasComment(a, "TAP-2", "from-b") {
		t.Fatal("handle a did not see b's committed TAP-2 comment")
	}
	if !hasComment(b, "TAP-1", "from-a") {
		t.Fatal("handle b did not see a's committed TAP-1 comment")
	}

	c, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !hasComment(c, "TAP-1", "from-a") || !hasComment(c, "TAP-2", "from-b") {
		t.Fatalf("third Open missing cross writes; TAP-1=%v TAP-2=%v",
			commentBodies(issueCommentTexts(c, "TAP-1")), commentBodies(issueCommentTexts(c, "TAP-2")))
	}
}

// TestDualOpenSameDBInterleavedWritesNoCorrupt: interleaved writes then a
// fresh Open must succeed and serve a consistent graph (not SQLITE_CORRUPT).
func TestDualOpenSameDBInterleavedWritesNoCorrupt(t *testing.T) {
	path := persistDBPath(t)
	a, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	applyTiny(t, a)
	b, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	for i := 0; i < 8; i++ {
		if _, err := a.AddComment("TAP-1", "", []byte(fmt.Sprintf("a-%d", i))); err != nil {
			t.Fatalf("a write %d: %v", i, err)
		}
		if _, err := b.AddComment("TAP-3", "", []byte(fmt.Sprintf("b-%d", i))); err != nil {
			t.Fatalf("b write %d: %v", i, err)
		}
	}

	c, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatalf("reopen after interleaved writes: %v", err)
	}
	defer c.Close()
	if c.Issue("TAP-1") == nil || c.Issue("TAP-3") == nil {
		t.Fatal("graph missing TAP-1 or TAP-3 after interleaved dual-open writes")
	}
	if n := len(c.Issue("TAP-1").Comments); n < 1 {
		t.Fatalf("TAP-1 comments=%d after interleaved writes", n)
	}
	if n := len(c.Issue("TAP-3").Comments); n < 1 {
		t.Fatalf("TAP-3 comments=%d after interleaved writes", n)
	}
}

func TestPersistPathYAMLRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	doc := []byte("issues:\n  - {key: OLD-1, summary: leftover yaml}\n")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err == nil {
		t.Fatal("expected PersistPath pointing at YAML to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "FixturePath") {
		t.Fatalf("YAML persist error must name FixturePath as the migration; got %q", msg)
	}
}

func TestPersistPathYAMLNotSilentlyOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	orig := []byte("issues:\n  - {key: KEEP-1, summary: do not clobber}\n")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err == nil {
		t.Fatal("expected YAML PersistPath to be refused")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("YAML PersistPath was overwritten; got:\n%s", got)
	}
}

func TestPersistEmptyFileRejected(t *testing.T) {
	path := persistDBPath(t)
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("expected empty PersistPath to be refused")
	}
	if !strings.Contains(err.Error(), "FixturePath") {
		t.Fatalf("empty file must use the YAML-migration error; got %q", err.Error())
	}
}

func TestPersistCorruptDBFailsLoud(t *testing.T) {
	path := persistDBPath(t)
	if err := os.WriteFile(path, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{PersistPath: path}); err == nil {
		t.Fatal("expected an error opening a corrupt persistence file")
	}
}

func TestPersistTruncatedSQLiteFailsLoud(t *testing.T) {
	path := persistDBPath(t)
	hdr := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte{0xff}, 40)...)
	if err := os.WriteFile(path, hdr, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{PersistPath: path}); err == nil {
		t.Fatal("expected an error opening a truncated SQLite header")
	}
}

func writeVersionedDB(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(workingSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
}

func TestPersistSchemaTooOldRejected(t *testing.T) {
	path := persistDBPath(t)
	writeVersionedDB(t, path, 0)
	_, err := Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("expected schema_version 0 to be rejected")
	}
	if !strings.Contains(err.Error(), "schema") && !strings.Contains(err.Error(), "user_version") {
		t.Fatalf("old-schema error should name schema/user_version; got %q", err.Error())
	}
}

func TestPersistSchemaTooNewRejected(t *testing.T) {
	path := persistDBPath(t)
	writeVersionedDB(t, path, 99)
	_, err := Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("expected schema_version 99 to be rejected")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("too-new schema error should include 99; got %q", err.Error())
	}
}

func TestSnapshotYAMLFromDBState(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	applyTiny(t, st)
	if _, err := st.AddComment("TAP-1", "", []byte("snap-me")); err != nil {
		t.Fatal(err)
	}
	doc := st.Snapshot()
	b, err := fixtures.MarshalYAML(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("TAP-1")) || !bytes.Contains(b, []byte("snap-me")) {
		t.Fatalf("Snapshot YAML missing persisted comment:\n%s", b)
	}
}

func TestSnapshotYAMLRoundTripAfterPersistWrite(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	applyTiny(t, st)
	if _, err := st.AddComment("TAP-1", "", []byte("round-trip")); err != nil {
		t.Fatal(err)
	}
	doc := st.Snapshot()
	st2 := New(Options{Seed: 1, Locale: locale.EN})
	defer st2.Close()
	if err := st2.Apply(doc); err != nil {
		t.Fatal(err)
	}
	if !hasComment(st2, "TAP-1", "round-trip") {
		t.Fatal("Snapshot YAML did not round-trip the persisted comment")
	}
}

func TestLocaleSurvivesSQLiteRestart(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetLocale(locale.KO); err != nil {
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
	if st2.Locale() != locale.KO {
		t.Fatalf("locale after restart = %s, want ko (must persist independently of Options.Locale)", st2.Locale())
	}
}

func TestMissingPersistFileSeedsThenCreatesDB(t *testing.T) {
	path := persistDBPath(t)
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	applyTiny(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected DB file to be created: %v", err)
	}
	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if st2.Issue("TAP-1") == nil {
		t.Fatal("seeded TAP-1 missing after reopen")
	}
}
