package store

// Attachment bytes are the one kind of record this store holds that has no
// upper bound a laptop can absorb: a real workspace's largest measured
// attachment is 884 MiB (gadak GDK-1617). SQLite cannot stream a BLOB —
// modernc.org/sqlite v1.40.1 has no incremental blob I/O, and reading a
// 100 MiB blob in 1 MiB substr() slices allocates 1.00x what reading it
// whole does — so a file-backed store keeps the bytes beside the database
// instead, one file per distinct content:
//
//	<blobDir>/tmp/<rand>        being written
//	<blobDir>/aa/aa3f9c…d21e    sha256, first two hex digits the bucket
//
// Content-addressed, so there are no filename rules to get wrong (an
// attachment filename is user data: spaces, slashes, emoji, reserved
// Windows names, case-insensitive collisions), identical bytes cost one
// file, and the name is its own checksum. attachment_blobs holds the
// reference; the path is always computed, never stored, so moving a
// workspace directory does not break it.
//
// A :memory: store has no directory to write into, so it keeps the old
// BLOB path. The split is persistence, not size.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ErrAttachmentTooLarge is returned when an upload exceeds the caller's
// cap. The cap is the caller's policy; the store only enforces the number
// it is handed, and refuses rather than truncating (gadak GDK-1614).
var ErrAttachmentTooLarge = errors.New("attachment is larger than the limit")

// blobRef is an attachment's byte-side identity: the row in
// attachment_blobs. Filename and MimeType are duplicated from the issue
// blob deliberately — serving /file/{media}/binary then needs no issue at
// all, where it used to scan every issue in the store for each request
// (once per view before, once per Range chunk now).
type blobRef struct {
	ID       string
	MediaID  string
	Filename string
	MimeType string
	SHA      string
	Size     int64
	// CreatedAt is when these bytes were stored — the attachment's own
	// timestamp, so a migrated row keeps the date it always had rather than
	// the date of the upgrade.
	CreatedAt string
}

// staged is bytes that are already durable (dir mode: written and fsynced
// under their content name) or in hand (:memory: mode), waiting only for
// their row. Staging happens with no store lock held: a 884 MiB upload
// under the write lock would stall every other write on a paired origin
// for the length of the transfer.
type staged struct {
	sha  string
	size int64
	body []byte // :memory: only
}

type blobs interface {
	// stage consumes r, at most max bytes (0 = unbounded), and returns
	// ErrAttachmentTooLarge if there is more.
	stage(r io.Reader, max int64) (staged, error)
	// commit makes the staged bytes reachable by ref. Called under the
	// store write lock, so it must be cheap.
	commit(ref blobRef, st staged) error
	load(ref blobRef) (io.ReadSeekCloser, error)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- :memory: — bytes stay in the attachments BLOB table ---

type memBlobs struct{ db *sql.DB }

func (m memBlobs) stage(r io.Reader, max int64) (staged, error) {
	b, err := readCapped(r, max)
	if err != nil {
		return staged{}, err
	}
	return staged{sha: hashOf(b), size: int64(len(b)), body: b}, nil
}

func (m memBlobs) commit(ref blobRef, st staged) error {
	body := st.body
	if body == nil {
		body = []byte{}
	}
	_, err := m.db.Exec(`INSERT OR REPLACE INTO attachments(id, bytes) VALUES(?,?)`, ref.ID, body)
	return err
}

func (m memBlobs) load(ref blobRef) (io.ReadSeekCloser, error) {
	var b []byte
	err := m.db.QueryRow(`SELECT bytes FROM attachments WHERE id=?`, ref.ID).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	return nopSeekCloser{newBytesReader(b)}, nil
}

// --- file-backed — one file per distinct content ---

// staleUploadAge is how long a file in tmp/ must sit untouched before it
// counts as debris. Longer than any plausible transfer, because the cost
// of waiting is disk and the cost of being wrong is a broken upload.
const staleUploadAge = 24 * time.Hour

type dirBlobs struct{ root string }

func newDirBlobs(root string) (dirBlobs, error) {
	d := dirBlobs{root: root}
	if err := os.MkdirAll(d.tmpDir(), 0o755); err != nil {
		return dirBlobs{}, fmt.Errorf("blobs: %w", err)
	}
	// Leftovers from an upload whose process died: rubbish, not orphans
	// (an orphan has a content name and may yet be referenced). Only ones
	// old enough that no upload could still be writing them — another
	// process may have this directory open and a transfer in flight, and
	// deleting its temp file breaks a live upload (measured under -race:
	// "rename …/tmp/up-…: no such file or directory").
	ents, err := os.ReadDir(d.tmpDir())
	if err == nil {
		cutoff := time.Now().Add(-staleUploadAge)
		for _, e := range ents {
			fi, err := e.Info()
			if err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(filepath.Join(d.tmpDir(), e.Name()))
			}
		}
	}
	return d, nil
}

func (d dirBlobs) tmpDir() string { return filepath.Join(d.root, "tmp") }

func (d dirBlobs) path(sha string) string {
	if len(sha) < 2 {
		return filepath.Join(d.root, "_", sha)
	}
	return filepath.Join(d.root, sha[:2], sha)
}

func (d dirBlobs) stage(r io.Reader, max int64) (st staged, err error) {
	f, err := os.CreateTemp(d.tmpDir(), "up-")
	if err != nil {
		return staged{}, fmt.Errorf("blobs: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			_ = os.Remove(tmp)
		}
	}()

	h := sha256.New()
	src := io.Reader(r)
	if max > 0 {
		src = io.LimitReader(r, max+1) // one past, so at-the-cap and over-it differ
	}
	n, err := io.Copy(io.MultiWriter(f, h), src)
	if err != nil {
		return staged{}, err
	}
	if max > 0 && n > max {
		return staged{}, ErrAttachmentTooLarge
	}
	if err = f.Sync(); err != nil {
		return staged{}, err
	}
	if err = f.Close(); err != nil {
		return staged{}, err
	}
	sha := hex.EncodeToString(h.Sum(nil))

	dst := d.path(sha)
	bucket := filepath.Dir(dst)
	_, bucketErr := os.Stat(bucket)
	if err = os.MkdirAll(bucket, 0o755); err != nil {
		return staged{}, fmt.Errorf("blobs: %w", err)
	}
	if bucketErr != nil {
		// The bucket is new, so its own entry in the root is not durable
		// yet. Syncing only the bucket makes the file durable inside a
		// directory that may not survive — and a row that commits against
		// a missing directory is a broken attachment, the one state this
		// design refuses. Only on creation: 1 of 256 uploads, not every
		// one.
		syncDir(d.root)
	}
	if err = os.Rename(tmp, dst); err != nil {
		// Defensive: Go's os.Rename replaces an existing target on every
		// platform (Windows goes through MoveFileEx with
		// MOVEFILE_REPLACE_EXISTING), so this should not fire. If some
		// filesystem does refuse, the target's name is its own checksum —
		// an existing file of the right size IS this content, and two
		// uploads of identical bytes race here legitimately.
		if fi, statErr := os.Stat(dst); statErr == nil && fi.Size() == n {
			_ = os.Remove(tmp)
			err = nil
		} else {
			return staged{}, err
		}
	}
	syncDir(filepath.Dir(dst)) // this file is the only copy
	return staged{sha: sha, size: n}, nil
}

// commit is a no-op: the file is already durable under its content name.
// The row lands afterwards, on purpose — a file with no row is an orphan
// that costs disk, a row with no file is a broken attachment.
func (d dirBlobs) commit(ref blobRef, st staged) error { return nil }

func (d dirBlobs) load(ref blobRef) (io.ReadSeekCloser, error) {
	return os.Open(d.path(ref.SHA))
}

func syncDir(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}

// readCapped reads all of r, refusing past max (0 = unbounded).
func readCapped(r io.Reader, max int64) ([]byte, error) {
	src := io.Reader(r)
	if max > 0 {
		src = io.LimitReader(r, max+1)
	}
	b, err := io.ReadAll(src)
	if err != nil {
		return nil, err
	}
	if max > 0 && int64(len(b)) > max {
		return nil, ErrAttachmentTooLarge
	}
	return b, nil
}

// nopSeekCloser gives in-memory bytes the same io.ReadSeekCloser shape a
// file has, so the serving path has one branch, not two.
type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func newBytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// AttachmentStorage is what an operator wants to know about attachment
// bytes without opening a shell on the machine that holds them: how many,
// how much disk, how far back, and whether any are unaccounted for.
//
// It is an aggregate over attachment_blobs, not a directory walk. That
// matters twice: the answer is one query rather than a stat per file, and
// it is reachable over the API — a paired workspace's bytes live on the
// home machine, where the client asking cannot walk anything.
type AttachmentStorage struct {
	// Attachments counts rows; Files counts distinct sha256, which is
	// smaller when the same bytes are attached more than once.
	Attachments int    `json:"attachments"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	LargestSize int64  `json:"largestSize"`
	OldestAt    string `json:"oldestAt,omitempty"`
	NewestAt    string `json:"newestAt,omitempty"`
	// Deleted rows are no longer referenced by any issue. There is no
	// delete verb yet, so this is 0; it exists so that when there is one,
	// "what can be reclaimed" is a question this struct already answers.
	Deleted int `json:"deleted"`
}

// AttachmentStorage reports the byte-side totals. Distinct-sha counting is
// why Files can be less than Attachments: the store is content-addressed.
//
// An error is returned, never swallowed into a zeroed struct: "0 bytes" and
// "the question could not be answered" are different answers, and a panel
// that shows the first when it means the second is worse than one that
// shows nothing.
func (s *Store) AttachmentStorage() (AttachmentStorage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out AttachmentStorage
	var oldest, newest sql.NullString
	var largest, total sql.NullInt64
	err := s.db.QueryRow(`
SELECT COUNT(*), COUNT(DISTINCT sha256), SUM(size), MAX(size), MIN(NULLIF(created_at,'')), MAX(created_at)
FROM attachment_blobs WHERE deleted_at=''`).
		Scan(&out.Attachments, &out.Files, &total, &largest, &oldest, &newest)
	if err != nil {
		return AttachmentStorage{}, err
	}
	out.Bytes, out.LargestSize = total.Int64, largest.Int64
	out.OldestAt, out.NewestAt = oldest.String, newest.String
	out.Deleted = s.sqlCount(`SELECT COUNT(*) FROM attachment_blobs WHERE deleted_at<>''`)
	return out, nil
}
