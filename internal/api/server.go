package api

import (
	"context"
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/midagedev/issuetap/internal/config"
	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/faults"
	"github.com/midagedev/issuetap/internal/store"
)

// Server is the HTTP surface: Atlassian dialects + dashboard API + embedded UI.
type Server struct {
	cfg  config.Config
	st   *store.Store
	eng  *faults.Engine
	log  *Log
	ui   fs.FS
	uiOK bool
}

// New builds a server. ui may be nil (dashboard still has JSON APIs).
func New(cfg config.Config, st *store.Store, eng *faults.Engine, ui fs.FS, uiOK bool) *Server {
	if eng == nil {
		eng = faults.New(nil)
	}
	return &Server{cfg: cfg, st: st, eng: eng, log: newLog(1000), ui: ui, uiOK: uiOK}
}

// Store is the live graph (scenarios mutate it).
func (s *Server) Store() *store.Store { return s.st }

// Engine is the live fault injector.
func (s *Server) Engine() *faults.Engine { return s.eng }

// Traces is the request log.
func (s *Server) Traces(limit int) []Trace { return s.log.list(limit) }

// LastTrace is the most recent request.
func (s *Server) LastTrace() *Trace { return s.log.last() }

// Handler is the root mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/", s.handleAdmin)
	mux.HandleFunc("/issuetap/avatar/", s.handleAvatar)
	mux.HandleFunc("/file/", s.handleMediaFile)
	mux.HandleFunc("/", s.handleRoot)
	return s.wrap(mux)
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 0}
		w.Header().Set("X-Issuetap", "1")
		w.Header().Set("X-Issuetap-Dialect", string(s.cfg.Dialect.Kind))
		w.Header().Set("X-Issuetap-Locale", string(s.st.Locale()))

		path := r.URL.Path
		hit := s.eng.Apply(r.Method, path)
		if hit.Delay > 0 {
			timer := time.NewTimer(hit.Delay)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		var reqBody string
		if r.Body != nil && r.ContentLength != 0 && r.ContentLength < 1<<20 {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			_ = r.Body.Close()
			reqBody = string(b)
			r.Body = io.NopCloser(strings.NewReader(reqBody))
		}

		if hit.Drift {
			r = r.WithContext(context.WithValue(r.Context(), ctxDrift{}, true))
		}
		if hit.Truncate > 0 {
			tw := &bufferResponse{h: make(http.Header)}
			writeFaultOrNext(tw, r, next, hit)
			flushTruncated(sw, tw, hit.Truncate)
		} else {
			writeFaultOrNext(sw, r, next, hit)
		}
		faultName := ""
		if hit.Fault != nil {
			faultName = hit.Fault.Name
			if faultName == "" {
				faultName = "unnamed"
			}
		}
		s.log.add(Trace{
			At:        start,
			Method:    r.Method,
			Path:      path,
			Query:     r.URL.RawQuery,
			Dialect:   string(s.cfg.Dialect.Kind),
			Status:    sw.code,
			LatencyMS: time.Since(start).Milliseconds(),
			Fault:     faultName,
			Bytes:     sw.bytes,
			Request:   clip(reqBody, 2048),
			Response:  clip(string(sw.buf), 2048),
		})
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Strip DC context path so handlers see /rest/api/2/...
	ctx := dialect.NormalizeContext(s.cfg.Dialect.ContextPath)
	if ctx != "" && (path == ctx || strings.HasPrefix(path, ctx+"/")) {
		path = strings.TrimPrefix(path, ctx)
		if path == "" {
			path = "/"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = path
		r = r2
	}

	if s.isAtlassian(path) {
		if !s.authorize(w, r) {
			return
		}
		if strings.HasPrefix(path, "/rest/dev-status/") {
			s.handleDevStatus(w, r, path)
			return
		}
		s.handleAtlassian(w, r, path)
		return
	}
	s.serveUI(w, r)
}

func (s *Server) isAtlassian(path string) bool {
	return strings.HasPrefix(path, "/rest/") || strings.HasPrefix(path, "/wiki/")
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if h == "" {
		writeAuth(w)
		return false
	}
	scheme, rest, _ := strings.Cut(h, " ")
	scheme = strings.TrimSpace(scheme)
	rest = strings.TrimSpace(rest)
	var user, secret string
	switch strings.ToLower(scheme) {
	case "basic":
		raw, err := base64.StdEncoding.DecodeString(rest)
		if err != nil {
			writeAuth(w)
			return false
		}
		user, secret, _ = strings.Cut(string(raw), ":")
	case "bearer":
		secret = rest
	default:
		writeAuth(w)
		return false
	}
	if !s.cfg.Accepts(scheme, user, secret) {
		writeAuth(w)
		return false
	}
	// Stash identity for writes.
	r.Header.Set("X-Issuetap-User", user)
	return true
}

func (s *Server) handleAtlassian(w http.ResponseWriter, r *http.Request, path string) {
	if MatchUnsupported(r.Method, path) {
		writeUnsupported(w, r.Method, path)
		return
	}
	// Confluence Cloud lives under /wiki; DC confluence under /rest/api
	// (same prefix as Jira DC but without /2).
	if strings.HasPrefix(path, "/wiki/") {
		s.handleConfluence(w, r, strings.TrimPrefix(path, "/wiki"))
		return
	}
	if s.cfg.Dialect.Kind == dialect.DC && isConfluenceDC(path) {
		s.handleConfluence(w, r, path)
		return
	}
	s.handleJira(w, r, path)
}

func isConfluenceDC(path string) bool {
	// /rest/api/space, /rest/api/content — not /rest/api/2 or /rest/api/3.
	if strings.HasPrefix(path, "/rest/api/2/") || strings.HasPrefix(path, "/rest/api/3/") {
		return false
	}
	if strings.HasPrefix(path, "/rest/api/space") || strings.HasPrefix(path, "/rest/api/content") {
		return true
	}
	return false
}

type ctxDrift struct{}

func driftOn(r *http.Request) bool {
	v, _ := r.Context().Value(ctxDrift{}).(bool)
	return v
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"product": "issuetap",
		"dialect": s.cfg.Dialect.Kind,
		"locale":  s.st.Locale(),
	})
}

func (s *Server) handleAvatar(w http.ResponseWriter, r *http.Request) {
	// 1×1 PNG so avatar URLs resolve without outbound calls.
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pixelPNG)
}

func (s *Server) handleMediaFile(w http.ResponseWriter, r *http.Request) {
	// The redirect target for attachment content: /file/{uuid}/binary.
	// Serves the stored bytes so a client that follows the 302 from
	// /attachment/content/{id} gets the uploaded file back.
	rest := strings.TrimPrefix(r.URL.Path, "/file/")
	media, _, _ := strings.Cut(rest, "/")
	body, a := s.st.AttachmentByMedia(media)
	if a == nil {
		http.NotFound(w, r)
		return
	}
	mime := a.MimeType
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if !s.uiOK || s.ui == nil {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(fallbackHTML))
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	f, err := s.ui.Open(p)
	if err != nil {
		// SPA fallback
		f, err = s.ui.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p = "index.html"
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, p, stat.ModTime(), strings.NewReader(string(b)))
		return
	}
	http.ServeContent(w, r, p, stat.ModTime(), rs)
}

func writeFaultOrNext(w http.ResponseWriter, r *http.Request, next http.Handler, hit faults.Hit) {
	if hit.Skip && hit.Fault != nil {
		if hit.Fault.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(hit.Fault.RetryAfter))
		}
		body := faults.BodyBytes(hit.Fault)
		code := hit.Fault.Status
		if code == 0 {
			code = http.StatusInternalServerError
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(body)
		return
	}
	next.ServeHTTP(w, r)
}

// bufferResponse holds a handler body so truncateBytes can cut it before
// the client sees it. Used only when a fault sets Truncate > 0.
type bufferResponse struct {
	h    http.Header
	code int
	buf  []byte
}

func (b *bufferResponse) Header() http.Header { return b.h }

func (b *bufferResponse) WriteHeader(code int) { b.code = code }

func (b *bufferResponse) Write(p []byte) (int, error) {
	if b.code == 0 {
		b.code = http.StatusOK
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func flushTruncated(dst http.ResponseWriter, src *bufferResponse, n int) {
	body := src.buf
	if n > 0 && len(body) > n {
		body = body[:n]
	}
	for k, vs := range src.h {
		dst.Header()[k] = vs
	}
	dst.Header().Set("Content-Length", strconv.Itoa(len(body)))
	code := src.code
	if code == 0 {
		code = http.StatusOK
	}
	dst.WriteHeader(code)
	if len(body) > 0 {
		_, _ = dst.Write(body)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// 1x1 transparent PNG.
var pixelPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

const fallbackHTML = `<!doctype html><meta charset="utf-8"><title>issuetap</title>
<body style="font:14px/1.4 ui-sans-serif,system-ui;margin:2rem;max-width:40rem">
<h1>issuetap</h1>
<p>Dashboard assets were not embedded. JSON APIs still work:</p>
<ul>
<li><a href="/healthz">/healthz</a></li>
<li><a href="/api/overview">/api/overview</a></li>
<li><a href="/api/requests">/api/requests</a></li>
<li><a href="/api/data">/api/data</a></li>
<li><a href="/api/compatibility">/api/compatibility</a></li>
</ul>
<p>Run <code>npm run build</code> and rebuild the Go binary to embed the UI.</p>
</body>`
