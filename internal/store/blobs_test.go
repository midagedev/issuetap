package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
)

// The cap applies to a :memory: store too. The API-level oversize test is
// file-backed (that is where real workspaces live), so without this the
// in-memory path had no cap coverage at all.
func TestInMemoryUploadRespectsTheCap(t *testing.T) {
	st := New(Options{Seed: 1})
	defer st.Close()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{'x'}, 1024)
	if _, err := st.AddAttachmentStream("TAP-1", "big.bin", "", "", bytes.NewReader(body), 512); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("over the cap gave %v, want ErrAttachmentTooLarge", err)
	}
	a, err := st.AddAttachmentStream("TAP-1", "ok.bin", "", "", bytes.NewReader(body), 1024)
	if err != nil {
		t.Fatalf("exactly at the cap must be accepted: %v", err)
	}
	got, _ := st.AttachmentBytes(a.ID)
	if !bytes.Equal(got, body) {
		t.Fatal("the accepted upload did not round-trip")
	}
}

// The point of attachment_blobs is that aggregate questions are one query.
// Before it, "how much disk do the attachments cost" meant decoding every
// issue blob and stat-ing every file, so nothing asked — and the app's own
// storage panel showed a number that excluded them entirely (gadak
// GDK-1617).
func TestAttachmentStorageAggregates(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(Options{Seed: 1, PersistPath: filepath.Join(dir, "issuetap.db")})
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
	base := st.AttachmentStorage()

	same := bytes.Repeat([]byte{'a'}, 4096)
	other := bytes.Repeat([]byte{'b'}, 100)
	for _, b := range [][]byte{same, same, other} {
		if _, err := st.AddAttachment("TAP-1", "f.bin", "application/octet-stream", "", b); err != nil {
			t.Fatal(err)
		}
	}
	got := st.AttachmentStorage()
	if got.Attachments != base.Attachments+3 {
		t.Errorf("attachments %d, want %d", got.Attachments, base.Attachments+3)
	}
	// Content-addressed: the duplicate is one file, not two.
	if got.Files != base.Files+2 {
		t.Errorf("distinct files %d, want %d — identical bytes must not count twice", got.Files, base.Files+2)
	}
	if got.Bytes != base.Bytes+int64(2*len(same)+len(other)) {
		t.Errorf("bytes %d, want %d", got.Bytes, base.Bytes+int64(2*len(same)+len(other)))
	}
	if got.LargestSize < int64(len(same)) {
		t.Errorf("largest %d, want at least %d", got.LargestSize, len(same))
	}
	if got.NewestAt == "" {
		t.Error("no timestamp on a row that was just written — created_at is not being set")
	}
}
