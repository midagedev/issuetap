package api

import (
	"net/http"
	"sync"
	"time"
)

// Trace is one captured request.
type Trace struct {
	ID        int           `json:"id"`
	At        time.Time     `json:"at"`
	Method    string        `json:"method"`
	Path      string        `json:"path"`
	Query     string        `json:"query,omitempty"`
	Dialect   string        `json:"dialect"`
	Status    int           `json:"status"`
	LatencyMS int64         `json:"latencyMs"`
	Fault     string        `json:"fault,omitempty"`
	Bytes     int           `json:"bytes"`
	Request   string        `json:"request,omitempty"`
	Response  string        `json:"response,omitempty"`
}

// Log is a bounded ring of traces.
type Log struct {
	mu   sync.Mutex
	next int
	max  int
	all  []Trace
}

func newLog(max int) *Log {
	if max <= 0 {
		max = 1000
	}
	return &Log{max: max}
}

func (l *Log) add(t Trace) Trace {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	t.ID = l.next
	l.all = append(l.all, t)
	if len(l.all) > l.max {
		l.all = l.all[len(l.all)-l.max:]
	}
	return t
}

func (l *Log) list(limit int) []Trace {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > len(l.all) {
		limit = len(l.all)
	}
	out := make([]Trace, limit)
	copy(out, l.all[len(l.all)-limit:])
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (l *Log) last() *Trace {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.all) == 0 {
		return nil
	}
	t := l.all[len(l.all)-1]
	return &t
}

type statusWriter struct {
	http.ResponseWriter
	code  int
	bytes int
	buf   []byte
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	w.bytes += len(p)
	if len(w.buf) < 4096 {
		need := 4096 - len(w.buf)
		if need > len(p) {
			need = len(p)
		}
		w.buf = append(w.buf, p[:need]...)
	}
	return w.ResponseWriter.Write(p)
}
