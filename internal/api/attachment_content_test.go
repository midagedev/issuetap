package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/midagedev/issuetap/internal/dialect"
)

// TestAttachmentDownloadServesUploadedBytes pins the 2026-08-17 finding:
// GET /attachment/content/{id} 302s to /file/{uuid}/binary, and the
// redirect target answered 200 with 0 bytes — the stored bytes never
// reached the client even before a restart, and after snapshot/restart
// they were gone entirely. A client that follows the redirect must get
// the exact uploaded bytes back.
func TestAttachmentDownloadServesUploadedBytes(t *testing.T) {
	ts := testServer(t, "en", dialect.Cloud)
	defer ts.Close()

	payload := []byte{0x00, 0x01, 0xfe, 0xff, 'r', 'a', 'w'}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "raw.bin")
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rest/api/3/issue/TAP-1/attachments", &buf)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload status %d: %s", res.StatusCode, b)
	}

	// The default client follows the 302 from content/{id} to /file/…/binary.
	dl := authGet(t, ts, "/rest/api/3/attachment/content/70001")
	defer dl.Body.Close()
	got, err := io.ReadAll(dl.Body)
	if err != nil {
		t.Fatal(err)
	}
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download status %d", dl.StatusCode)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("downloaded %d bytes (%q), want the %d uploaded bytes", len(got), got, len(payload))
	}
}
