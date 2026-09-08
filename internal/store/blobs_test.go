package store

import (
	"bytes"
	"errors"
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
