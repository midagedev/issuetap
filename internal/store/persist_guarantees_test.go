package store

// gadak GDK-243. On a built-in gadak workspace this persist file is the
// only copy of the user's data, so the open path owes three guarantees
// beyond "it works": a refusal never touches the bytes, a forward
// migration never loses a row, and every refusal says which version it
// found, which it reads, and what to do next.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/model"
)

// markPersist drops a recognizable row into a persist file and stamps it
// at `version`. Byte identity is the wrong contract for "the refusal left
// it alone" — merely opening a WAL database checkpoints it and moves the
// header — so the guarantee is read back as content: the stamp and the row
// are still there afterwards.
func markPersist(t *testing.T, path string, version int) {
	t.Helper()
	writeVersionedDB(t, path, version)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO issues(key, id, blob) VALUES('TAP-9','10009',?)`,
		encodeIssue(&model.Issue{ID: "10009", Key: "TAP-9", Summary: "do not lose me", ProjectKey: "TAP"})); err != nil {
		t.Fatal(err)
	}
}

func assertPersistIntact(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var have int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		t.Fatal(err)
	}
	if have != version {
		t.Errorf("the refusal restamped the persist: user_version %d, want %d", have, version)
	}
	var summary string
	if err := db.QueryRow(`SELECT json_extract(blob, '$.summary') FROM issues WHERE key='TAP-9'`).Scan(&summary); err != nil {
		t.Fatalf("the row is gone after the refusal — the file was emptied or re-created: %v", err)
	}
	if summary != "do not lose me" {
		t.Errorf("the row came back changed: %q", summary)
	}
}

// A persist written by a newer build is refused. The bytes must be exactly
// as they were: the failure mode this guards is "opened, could not read,
// re-created empty", which silently destroys a workspace.
func TestRefusingANewerPersistLeavesTheBytesAlone(t *testing.T) {
	path := persistDBPath(t)
	markPersist(t, path, persistSchemaVersion+1)

	_, err := Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("a persist stamped newer than this build must be refused")
	}
	assertPersistIntact(t, path, persistSchemaVersion+1)
	msg := err.Error()
	for _, want := range []string{
		fmt.Sprintf("%d", persistSchemaVersion+1), // what it found
		fmt.Sprintf("%d", persistSchemaVersion),   // what this build reads
		"upgrade",                                 // what to do
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q: %s", want, msg)
		}
	}
}

// An unstamped SQLite file (user_version 0) is not something any issuetap
// build wrote, so it is refused rather than migrated blind. The refusal
// still owes both versions and a next step, and must not touch the file.
func TestRefusingAnUnstampedPersistIsHonestAndNonDestructive(t *testing.T) {
	path := persistDBPath(t)
	markPersist(t, path, 0)

	_, err := Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("an unstamped persist must be refused")
	}
	assertPersistIntact(t, path, 0)
	msg := err.Error()
	for _, want := range []string{"schema_version 0", fmt.Sprintf("%d", persistSchemaVersion), path} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "newer") {
		t.Errorf("an unstamped file was not written by a newer build; the refusal says it was: %s", msg)
	}
}

// A SQLite file this build can read the stamp of but which holds no
// issuetap graph is somebody else's database. Saying "written by a newer
// gadak" sends them to upgrade a binary that is already current, and
// points at a pre-upgrade copy that was never taken.
func TestRefusingAForeignDatabaseDoesNotBlameTheVersion(t *testing.T) {
	path := persistDBPath(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", persistSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = Open(Options{PersistPath: path})
	if err == nil {
		t.Fatal("a SQLite file with no issuetap graph must be refused")
	}
	msg := err.Error()
	if strings.Contains(msg, "newer") {
		t.Errorf("this file is not from a newer build; the refusal says it is: %s", msg)
	}
	if strings.Contains(msg, backupPath(path, persistSchemaVersion)) {
		t.Errorf("the refusal points at a pre-upgrade copy that was never taken: %s", msg)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("the refusal does not name the file: %s", msg)
	}
}

// Forward migration is lossless for rows the migration does not otherwise
// touch. v1 -> v3 rewrites the attachment side; the issue graph must come
// out the other end unchanged, and the new v3 tables must exist.
func TestForwardMigrationKeepsTheIssueGraph(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	writeV1Persist(t, path, map[string][]byte{"70001": []byte("payload")})

	st, err := Open(Options{Seed: 1, PersistPath: path})
	if err != nil {
		t.Fatalf("opening a v1 persist must migrate it: %v", err)
	}
	defer st.Close()

	iss := st.Issue("TAP-1")
	if iss == nil {
		t.Fatal("TAP-1 did not survive the migration")
	}
	if iss.Summary != "seeded" || iss.ProjectKey != "TAP" || iss.ID != "10001" {
		t.Fatalf("the issue came back changed: %+v", iss)
	}
	var ver int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != persistSchemaVersion {
		t.Fatalf("user_version %d after migrating, want %d", ver, persistSchemaVersion)
	}
	for _, tbl := range []string{"boards", "sprints", "attachment_blobs"} {
		var name string
		if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&name); err != nil {
			t.Errorf("table %s is missing after migrating to v%d: %v", tbl, persistSchemaVersion, err)
		}
	}
}
