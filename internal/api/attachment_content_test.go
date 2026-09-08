package api_test

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"path/filepath"

	"github.com/midagedev/issuetap/internal/api"
	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/store"
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

// TestOversizeAttachmentIsRefusedNotTruncated pins gadak GDK-1614. The
// upload read `io.ReadAll(io.LimitReader(part, 8<<20))`, and that pair
// stops at the limit with no error — so a larger file was stored as its
// first 8 MiB, the response said 200, and the recorded size was the
// truncated one. Measured in gadak: 12,582,912 bytes in, 8,388,608 stored,
// different hashes, `gadak attach` printing success.
//
// The origin holds the only copy, so a silent truncation is unrecoverable
// data loss. Refusing is the contract; the cap itself is a separate
// question from how the bytes are stored.
func TestOversizeAttachmentIsRefusedNotTruncated(t *testing.T) {
	// A file-backed server with a small cap: the cap is configuration now
	// (gadak GDK-1617), so the test states the number it is testing
	// instead of allocating a gigabyte to reach a constant.
	const capBytes = 1 << 20
	ts := testServerCapped(t, capBytes)
	defer ts.Close()

	payload := bytes.Repeat([]byte{'A'}, capBytes+1)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "oversize.bin")
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
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusOK {
		t.Fatalf("an oversize upload answered 200 — it was truncated and reported as success: %s", body)
	}
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413: %s", res.StatusCode, body)
	}
	// The refusal has to say what the limit is, or the person cannot act on it.
	if !bytes.Contains(body, []byte(fmt.Sprintf("%d MiB", capBytes>>20))) {
		t.Errorf("the 413 does not name the cap: %s", body)
	}
}

// testServerCapped is a file-backed server (attachment bytes on disk, the
// path every real workspace takes) with an explicit upload cap.
func testServerCapped(t *testing.T, max int64) *httptest.Server {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st, err := store.Open(store.Options{Seed: 1, Locale: "en", PersistPath: filepath.Join(dir, "issuetap.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Dialect.Kind = dialect.Cloud
	cfg.MaxAttachmentBytes = max
	return httptest.NewServer(api.New(cfg, st, nil, nil, false).Handler())
}

// TestAttachmentContentSupportsRangeAndETag pins gadak GDK-1616. The media
// route wrote the whole blob with `w.Write` — no Accept-Ranges, no ETag, no
// explicit Content-Length. A browser's <video> seeks with Range, so seeking
// was dead in gadak's app and some formats refused to play at all. The
// mime the upload recorded was on the attachment row the whole time.
func TestAttachmentContentSupportsRangeAndETag(t *testing.T) {
	ts := testServer(t, "en", dialect.Cloud)
	defer ts.Close()

	payload := []byte("0123456789abcdefghij")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="clip.mp4"`)
	h.Set("Content-Type", "video/mp4")
	fw, _ := mw.CreatePart(h)
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
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", res.StatusCode)
	}

	full := authGet(t, ts, "/rest/api/3/attachment/content/70001")
	defer full.Body.Close()
	if got := full.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes — a video cannot be seeked without it", got)
	}
	etag := full.Header.Get("ETag")
	if etag == "" {
		t.Error("no ETag: every view re-downloads the whole file")
	}
	if got := full.Header.Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q, want the mime the upload recorded", got)
	}
	if got := full.Header.Get("Content-Length"); got != "20" {
		t.Errorf("Content-Length = %q, want 20", got)
	}

	// A range request must answer 206 with exactly those bytes.
	part := authGetRange(t, ts, "/rest/api/3/attachment/content/70001", "bytes=5-9")
	defer part.Body.Close()
	if part.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status %d, want 206", part.StatusCode)
	}
	gotPart, _ := io.ReadAll(part.Body)
	if string(gotPart) != "56789" {
		t.Errorf("range body = %q, want %q", gotPart, "56789")
	}

	// The bytes behind an id never change, so the ETag has to be stable.
	again := authGet(t, ts, "/rest/api/3/attachment/content/70001")
	defer again.Body.Close()
	if got := again.Header.Get("ETag"); got != etag {
		t.Errorf("ETag moved between two views: %q then %q", etag, got)
	}
}

// authGetRange is authGet with a Range header (GDK-1616).
func authGetRange(t *testing.T, ts *httptest.Server, path, rng string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.SetBasicAuth("you@example.com", "issuetap")
	req.Header.Set("Range", rng)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
