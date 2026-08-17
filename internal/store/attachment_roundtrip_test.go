package store

import (
	"bytes"
	"testing"

	"github.com/midagedev/issuetap/internal/locale"
)

// TestAttachmentBytesSurviveSnapshotRoundTrip pins the defect observed on
// 2026-08-17: an uploaded attachment's metadata (filename) survives
// Snapshot → Apply, but the content GET returned 200 with 0 bytes because
// the original bytes never entered the fixture document.
func TestAttachmentBytesSurviveSnapshotRoundTrip(t *testing.T) {
	st := loadTiny(t)
	body := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0xff, 0xfe, 'b', 'i', 'n', 'a', 'r', 'y'}
	a, err := st.AddAttachment("TAP-1", "crash.bin", "application/octet-stream", "", body)
	if err != nil {
		t.Fatal(err)
	}
	doc := st.Snapshot()
	st2 := New(Options{Seed: st.Seed(), Locale: locale.EN})
	if err := st2.Apply(doc); err != nil {
		t.Fatal(err)
	}
	got, att := st2.AttachmentBytes(a.ID)
	if att == nil {
		t.Fatalf("attachment %s lost metadata after round-trip", a.ID)
	}
	if att.Size != int64(len(body)) {
		t.Fatalf("size after round-trip = %d, want %d", att.Size, len(body))
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("bytes after round-trip = %q, want %q", got, body)
	}
}

// TestAttachmentTextFixtureStillReadable guards the authored-fixture path:
// a plain-text attachment declared inline in YAML must keep serving the
// authored bytes, not a placeholder.
func TestAttachmentTextFixtureStillReadable(t *testing.T) {
	st := loadTiny(t)
	b, a := st.AttachmentBytes("7001")
	if a == nil {
		t.Fatal("fixture attachment 7001 missing")
	}
	if string(b) != "panic: flangewidget" {
		t.Fatalf("fixture attachment bytes = %q", b)
	}
}

// TestSnapshotReadableAttachmentKeepsTextAxis is the format-decision
// contract: printable UTF-8 attachment content snapshots as readable
// inline text so the document stays human-readable; binary content uses
// the base64 field. Whatever the encoding, bytes must round-trip.
func TestSnapshotReadableAttachmentKeepsTextAxis(t *testing.T) {
	st := loadTiny(t)
	if _, err := st.AddAttachment("TAP-1", "note.txt", "text/plain", "", []byte("readable log line")); err != nil {
		t.Fatal(err)
	}
	doc := st.Snapshot()
	found := false
	for _, iss := range doc.Issues {
		if iss.Key != "TAP-1" {
			continue
		}
		for _, a := range iss.Attachments {
			if a.Filename != "note.txt" {
				continue
			}
			found = true
			if a.Text != "readable log line" {
				t.Fatalf("readable attachment snapshotted as %q, want inline text", a.Text)
			}
		}
	}
	if !found {
		t.Fatal("uploaded note.txt not in snapshot")
	}
}
