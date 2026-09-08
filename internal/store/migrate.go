package store

// Persist migrations. There was exactly one schema version for the life
// of this store, and a mismatch was refused; v2 is the first forward step,
// so this file is also the pattern for the next one.
//
// v1 → v2 moves attachment bytes out of the `attachments` BLOB table and
// into <blobDir> (see blobs.go). The rules that make it safe to run on a
// file someone cares about:
//
//   - the pre-migration copy is taken with VACUUM INTO, never a file copy.
//     The persist is WAL, so copying the .db alone yields a file missing
//     committed pages — a broken backup at exactly the moment it is needed.
//   - the bytes are written to disk BEFORE the transaction opens. File
//     writes do not roll back; a failure after them leaves orphan files,
//     which cost disk and nothing else.
//   - the transaction is BEGIN IMMEDIATE and re-reads user_version inside
//     itself. One persist is shared by every process that opened it
//     (gadak GDK-1180), so the CLI and a running serve can reach v1 at the
//     same moment; the loser sees 2 and stops.
//   - `attachments` is emptied, not dropped, so a migrated file and a fresh
//     one have the same schema text. VACUUM afterwards returns the space.
//
// v2 → v3 is the other shape a migration can be: pure DDL (agileSchema,
// for the boards/sprints tables — gadak GDK-1666). Nothing is moved or
// dropped, so there is no VACUUM INTO backup: SQLite DDL is transactional,
// a failure rolls the CREATEs back and leaves a v2 file untouched, and
// re-running after an unclean exit is idempotent (the version re-check
// stops a loser whose tables already exist).

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// backupPath names the copy taken before migrating to version `to`.
func backupPath(persist string, to int) string {
	return fmt.Sprintf("%s.pre-v%d.bak", persist, to)
}

func migratePersist(db *sql.DB, path string, have int, blobDir string) error {
	if have < 1 {
		return fmt.Errorf("persist %s: schema_version %d is not a version this build can migrate", path, have)
	}
	if have < 2 {
		if err := migrateV1toV2(db, path, blobDir); err != nil {
			return fmt.Errorf("persist %s: migrate to schema_version 2: %w", path, err)
		}
	}
	if have < 3 {
		if err := migrateV2toV3(db, path); err != nil {
			return fmt.Errorf("persist %s: migrate to schema_version 3: %w", path, err)
		}
	}
	return nil
}

func migrateV1toV2(db *sql.DB, path, blobDir string) error {
	if blobDir == "" {
		return fmt.Errorf("no attachment directory was configured; this build stores attachment bytes beside the database")
	}
	// Cheap early out: another process may already have finished while
	// this one was queued behind it.
	var have int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&have); err == nil && have >= 2 {
		return nil
	}
	size, err := fileSize(path)
	if err != nil {
		return err
	}
	// Backup + extracted blobs + the VACUUM temporary, each about the
	// size of the current file. Say the number rather than dying on ENOSPC
	// halfway.
	if err := requireFreeSpace(path, size*3); err != nil {
		return err
	}

	// VACUUM INTO a unique name, then rename into place. Two processes can
	// reach v1 at the same moment (that is the whole reason for the
	// version re-check below), and a shared target means one of them
	// vacuums into the other's half-written file — measured, as
	// "table users already exists".
	bak := backupPath(path, 2)
	tmpBak, err := os.CreateTemp(filepath.Dir(path), filepath.Base(bak)+".*")
	if err != nil {
		return err
	}
	tmpBakPath := tmpBak.Name()
	tmpBak.Close()
	_ = os.Remove(tmpBakPath) // VACUUM INTO refuses an existing target
	if _, err := db.Exec(`VACUUM INTO ?`, tmpBakPath); err != nil {
		return fmt.Errorf("pre-migration copy %s: %w", bak, err)
	}
	// Re-read the version with the snapshot in hand: another process may
	// have finished migrating between the check at the top and now, which
	// would make this snapshot a POST-migration one — a v2 file with an
	// empty attachments table. Renaming that over the backup replaces the
	// rollback copy with a workspace that has no attachments, which is
	// worse than having no backup at all, because the runbook tells people
	// to restore from it.
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&have); err == nil && have >= 2 {
		_ = os.Remove(tmpBakPath)
		return nil
	}
	// And never replace a backup that is already there: os.Rename would,
	// on every platform. An existing .pre-v2.bak was taken before some
	// earlier attempt, so by construction it is a v1 copy — the one we
	// want.
	if _, err := os.Stat(bak); err == nil {
		_ = os.Remove(tmpBakPath)
	} else if err := os.Rename(tmpBakPath, bak); err != nil {
		_ = os.Remove(tmpBakPath)
		return fmt.Errorf("pre-migration copy %s: %w", bak, err)
	}

	meta, err := attachmentMetaFromIssues(db)
	if err != nil {
		return err
	}
	bl, err := newDirBlobs(blobDir)
	if err != nil {
		return err
	}

	ids, err := scanStrings(db, `SELECT id FROM attachments ORDER BY id`)
	if err != nil {
		return err
	}
	refs := make([]blobRef, 0, len(ids))
	var truncated []string
	for _, id := range ids {
		var body []byte
		if err := db.QueryRow(`SELECT bytes FROM attachments WHERE id=?`, id).Scan(&body); err != nil {
			return fmt.Errorf("read attachment %s: %w", id, err)
		}
		st, err := bl.stage(bytes.NewReader(body), 0)
		if err != nil {
			return fmt.Errorf("write attachment %s: %w", id, err)
		}
		ref := meta[id]
		ref.ID, ref.SHA, ref.Size = id, st.sha, st.size
		if ref.MediaID == "" {
			// An attachment row whose issue no longer lists it. Keep the
			// bytes reachable by id; uuid5 is what the issue would have.
			ref.MediaID = uuid5(id)
		}
		if st.size == 8<<20 {
			// The old cap truncated silently before GDK-1614; a file that
			// is exactly 8 MiB is a candidate (gadak GDK-1615).
			truncated = append(truncated, id)
		}
		refs = append(refs, ref)
	}

	tx, err := db.Begin() // _txlock=immediate: this is BEGIN IMMEDIATE
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var now int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&now); err != nil {
		return err
	}
	if now >= 2 {
		return nil // another process got here first
	}
	if _, err := tx.Exec(attachmentBlobsSchema); err != nil {
		return err
	}
	for _, r := range refs {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO attachment_blobs(id, media_id, filename, mime, sha256, size, created_at) VALUES(?,?,?,?,?,?,?)`,
			r.ID, r.MediaID, r.Filename, r.MimeType, r.SHA, r.Size, r.CreatedAt); err != nil {
			return err
		}
	}
	// Delete only the ids that were staged, never the whole table. An
	// older binary can still have this persist open — the open marker is
	// advisory and multi-process persist is supported (gadak GDK-1180) —
	// and it goes on writing uploads into `attachments` as BLOBs while
	// this runs. Extraction takes minutes on a workspace of any size, so
	// a row inserted after the scan at the top of this function was never
	// staged: `DELETE FROM attachments` would destroy the only copy of it.
	// Leaving it means the bytes are still in the file, recoverable by
	// hand, instead of gone.
	for _, r := range refs {
		if _, err := tx.Exec(`DELETE FROM attachments WHERE id=?`, r.ID); err != nil {
			return err
		}
	}
	if n, err := txCount(tx, `SELECT COUNT(*) FROM attachments`); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "issuetap: %d attachment row(s) appeared while migrating and were left in place — another process is writing to this persist with an older build; stop it and reopen\n", n)
	}
	if _, err := tx.Exec(`PRAGMA user_version = 2`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Separate pass, and its failure is not the migration's: the data is
	// already correct, the file is just still large.
	_, _ = db.Exec(`VACUUM`)
	if len(truncated) > 0 {
		fmt.Fprintf(os.Stderr, "issuetap: %d attachment(s) are exactly 8 MiB and may have been truncated by the old cap: %s\n",
			len(truncated), strings.Join(truncated, ", "))
	}
	return nil
}

// migrateV2toV3 adds the agile tables (boards, sprints) to a v2 persist.
// Same BEGIN IMMEDIATE + in-tx version re-read as v1 → v2 — the persist is
// shared, and the loser of a simultaneous upgrade must stop, not error on
// "table boards already exists".
func migrateV2toV3(db *sql.DB, path string) error {
	var have int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&have); err == nil && have >= 3 {
		return nil
	}
	tx, err := db.Begin() // _txlock=immediate: this is BEGIN IMMEDIATE
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&have); err != nil {
		return err
	}
	if have >= 3 {
		return nil // another process got here first
	}
	if _, err := tx.Exec(agileSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 3`); err != nil {
		return err
	}
	return tx.Commit()
}

// attachmentMetaFromIssues reads filename/mime/media for every attachment
// the issues list. The migration walks the issues once; serving never does
// again.
func attachmentMetaFromIssues(db *sql.DB) (map[string]blobRef, error) {
	rows, err := db.Query(`SELECT blob FROM issues`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]blobRef{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		for _, a := range jsonDecode[storedIssue](b).Attachments {
			out[a.ID] = blobRef{ID: a.ID, MediaID: a.MediaID, Filename: a.Filename,
				MimeType: a.MimeType, CreatedAt: a.Created}
		}
	}
	return out, rows.Err()
}

func txCount(tx *sql.Tx, q string) (int, error) {
	var n int
	err := tx.QueryRow(q).Scan(&n)
	return n, err
}

func scanStrings(db *sql.DB, q string) ([]string, error) {
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func dirOf(path string) string { return filepath.Dir(path) }

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
