package store

// The v1 → v2 migration is the first this persist has ever had: for the
// whole life of the file a version mismatch was refused outright, so
// nothing here had a precedent to copy. These tests are the contract.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/midagedev/issuetap/internal/model"
)

// writeV1Persist builds a database exactly as the pre-migration binary
// left it: no attachment_blobs table, bytes in the attachments BLOB, and
// PRAGMA user_version = 1.
func writeV1Persist(t *testing.T, path string, attach map[string][]byte) {
	t.Helper()
	db, err := sql.Open("sqlite", persistDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	v1Schema := strings.TrimPrefix(workingSchema, attachmentBlobsSchema)
	if v1Schema == workingSchema {
		t.Fatal("workingSchema no longer starts with attachmentBlobsSchema — this fixture is not v1")
	}
	if _, err := db.Exec(v1Schema); err != nil {
		t.Fatal(err)
	}

	iss := &model.Issue{ID: "10001", Key: "TAP-1", Summary: "seeded", ProjectKey: "TAP"}
	for id, body := range attach {
		if _, err := db.Exec(`INSERT INTO attachments(id, bytes) VALUES(?,?)`, id, body); err != nil {
			t.Fatal(err)
		}
		iss.Attachments = append(iss.Attachments, model.Attachment{
			ID: id, Filename: id + ".bin", MimeType: "application/octet-stream",
			Size: int64(len(body)), MediaID: uuid5(id),
		})
	}
	if _, err := db.Exec(`INSERT INTO issues(key, id, blob) VALUES(?,?,?)`, iss.Key, iss.ID, encodeIssue(iss)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestV1PersistMigratesBytesToDiskAndReadsTheSame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	body := bytes.Repeat([]byte{0x00, 0x01, 0xfe, 0xff}, 4096)
	writeV1Persist(t, path, map[string][]byte{"70001": body})

	st, err := Open(Options{Seed: 1, PersistPath: path})
	if err != nil {
		t.Fatalf("opening a v1 persist must migrate it, not refuse it: %v", err)
	}
	defer st.Close()

	got, a := st.AttachmentBytes("70001")
	if a == nil {
		t.Fatal("the attachment is gone from the issue after migrating")
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("read back %d bytes, want the %d that were there", len(got), len(body))
	}

	// The bytes are on disk under their content name...
	blob := filepath.Join(dir, "blobs", sha(body)[:2], sha(body))
	if fi, err := os.Stat(blob); err != nil || fi.Size() != int64(len(body)) {
		t.Fatalf("no content-addressed file at %s: %v", blob, err)
	}
	// ...and no longer in the database.
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("attachments still holds %d row(s); the file did not shrink", n)
	}
	var ver int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != persistSchemaVersion {
		t.Errorf("user_version %d, want %d", ver, persistSchemaVersion)
	}
	// The pre-migration copy is kept: it is the only way back to an older
	// gadak, and it is what persistSchemaError points at.
	if _, err := os.Stat(backupPath(path, 2)); err != nil {
		t.Errorf("no pre-migration copy at %s: %v", backupPath(path, 2), err)
	}
}

func TestMigrationServesTheSameBytesThroughTheMediaRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	body := []byte("seeking in a video needs a seekable reader")
	writeV1Persist(t, path, map[string][]byte{"70001": body})

	st, err := Open(Options{Seed: 1, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rc, info, ok := st.OpenAttachmentByMedia(uuid5("70001"))
	if !ok {
		t.Fatal("the media lookup does not resolve after migrating")
	}
	defer rc.Close()
	if info.SHA256 != sha(body) {
		t.Errorf("SHA256 %q, want the content hash %q", info.SHA256, sha(body))
	}
	if info.Filename != "70001.bin" || info.MimeType != "application/octet-stream" {
		t.Errorf("filename/mime did not survive the migration: %+v", info)
	}
}

func TestIdenticalBytesBecomeOneFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	body := []byte("the same screenshot attached twice")
	writeV1Persist(t, path, map[string][]byte{"70001": body, "70002": body})

	st, err := Open(Options{Seed: 1, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var files int
	_ = filepath.Walk(filepath.Join(dir, "blobs"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && !strings.Contains(p, string(os.PathSeparator)+"tmp"+string(os.PathSeparator)) {
			files++
		}
		return nil
	})
	if files != 1 {
		t.Fatalf("%d blob files for two attachments of identical bytes, want 1", files)
	}
	for _, id := range []string{"70001", "70002"} {
		if got, _ := st.AttachmentBytes(id); !bytes.Equal(got, body) {
			t.Errorf("%s reads back wrong after dedup", id)
		}
	}
}

// A failed migration must leave the file openable by the old binary — that
// is, still v1, with its bytes still in the BLOB — and a retry must work.
func TestFailedMigrationLeavesV1IntactAndRetrySucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	body := []byte("bytes that must not be lost by a half-migration")
	writeV1Persist(t, path, map[string][]byte{"70001": body})

	// A blob directory that cannot be created: a plain file in its place.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Seed: 1, PersistPath: path, BlobDir: blocked}); err == nil {
		t.Fatal("a migration that cannot write its bytes must fail, not report success")
	}

	var ver int
	db, err := sql.Open("sqlite", persistDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if ver != 1 {
		t.Fatalf("user_version %d after a failed migration, want 1 — the old binary can no longer open its own file", ver)
	}

	st, err := Open(Options{Seed: 1, PersistPath: path})
	if err != nil {
		t.Fatalf("the retry must succeed: %v", err)
	}
	defer st.Close()
	if got, _ := st.AttachmentBytes("70001"); !bytes.Equal(got, body) {
		t.Fatal("the bytes did not survive a failed migration followed by a retry")
	}
}

// One persist is shared by every process that opened it (gadak GDK-1180),
// so two can reach v1 at the same moment. Both must end up working. This
// says nothing about how many times the bytes were written — content
// addressing makes a second write land on the same file — and everything
// about neither open being corrupted by the other. Two real races came
// out of it: a shared VACUUM INTO target, and the tmp sweep deleting the
// other process's in-flight upload.
func TestConcurrentOpensBothSucceed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issuetap.db")
	body := []byte("two processes, one file")
	writeV1Persist(t, path, map[string][]byte{"70001": body})

	var wg sync.WaitGroup
	stores := make([]*Store, 2)
	errs := make([]error, 2)
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stores[i], errs[i] = Open(Options{Seed: 1, PersistPath: path})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d failed: %v", i, err)
		}
		defer stores[i].Close()
		if got, _ := stores[i].AttachmentBytes("70001"); !bytes.Equal(got, body) {
			t.Errorf("open %d cannot read the attachment", i)
		}
	}
}

// A file from a NEWER gadak cannot be guessed at. The refusal has to name
// the way back, or the person is stuck with a database they cannot open
// and no idea that a copy of the old one exists.
func TestNewerSchemaRefusalNamesTheBackup(t *testing.T) {
	err := persistSchemaError("/w/issuetap.db", persistSchemaVersion+1, persistSchemaVersion)
	if !strings.Contains(err.Error(), backupPath("/w/issuetap.db", persistSchemaVersion+1)) {
		t.Fatalf("the refusal does not name the pre-upgrade copy: %v", err)
	}
}
