// Package faults is first-class, deterministic fault injection.
//
// A scenario declares faults; the server applies them in declaration order
// to matching requests. Counts are per-process and start at 1 for the first
// request that matches the fault's filter.
package faults

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Fault is one injection rule.
type Fault struct {
	// Name is shown on the dashboard and in traces.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// After is the 1-based matching request index at which the fault starts.
	// 0 means "from the first matching request".
	After int `json:"after,omitempty" yaml:"after,omitempty"`
	// Times is how many matching requests the fault applies to after it
	// starts. 0 means forever.
	Times int `json:"times,omitempty" yaml:"times,omitempty"`
	// Method, when set, must match (case-insensitive).
	Method string `json:"method,omitempty" yaml:"method,omitempty"`
	// PathPrefix matches the request URL path (or path without query).
	PathPrefix string `json:"pathPrefix,omitempty" yaml:"pathPrefix,omitempty"`
	// PathContains is a substring match on the path.
	PathContains string `json:"pathContains,omitempty" yaml:"pathContains,omitempty"`
	// Status is the HTTP status to return. 0 with Delay only slows the request.
	Status int `json:"status,omitempty" yaml:"status,omitempty"`
	// RetryAfter is the Retry-After header value in seconds (429/503).
	RetryAfter int `json:"retryAfter,omitempty" yaml:"retryAfter,omitempty"`
	// Delay is slept before the response. Parsed as a Go duration.
	Delay string `json:"delay,omitempty" yaml:"delay,omitempty"`
	// Body is returned instead of the handler's body. A string is sent as-is;
	// any other JSON value is marshaled.
	Body any `json:"body,omitempty" yaml:"body,omitempty"`
	// Malformed, when true, writes a truncated / non-JSON body.
	Malformed bool `json:"malformed,omitempty" yaml:"malformed,omitempty"`
	// TruncateBytes cuts the real handler body to N bytes (partial page).
	TruncateBytes int `json:"truncateBytes,omitempty" yaml:"truncateBytes,omitempty"`
	// Drift enables startAt pagination drift (DC). Cloud nextPageToken is
	// stable; this flag is a no-op there.
	Drift bool `json:"drift,omitempty" yaml:"drift,omitempty"`
}

// Hit is the result of applying the engine to one request.
type Hit struct {
	Fault     *Fault
	Delay     time.Duration
	Skip      bool // true → do not call the handler
	Drift     bool
	Truncate  int
}

// Engine is the live injector.
type Engine struct {
	mu     sync.Mutex
	faults []Fault
	// seen[i] is how many requests have matched faults[i].
	seen []int
	// applied[i] is how many times faults[i] has fired.
	applied []int
	total   int
}

// New builds an engine.
func New(fs []Fault) *Engine {
	cp := append([]Fault{}, fs...)
	return &Engine{faults: cp, seen: make([]int, len(cp)), applied: make([]int, len(cp))}
}

// Reset clears counters. Faults stay.
func (e *Engine) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = make([]int, len(e.faults))
	e.applied = make([]int, len(e.faults))
	e.total = 0
}

// Replace swaps the fault list and resets counters.
func (e *Engine) Replace(fs []Fault) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.faults = append([]Fault{}, fs...)
	e.seen = make([]int, len(e.faults))
	e.applied = make([]int, len(e.faults))
	e.total = 0
}

// List is a copy of the configured faults.
func (e *Engine) List() []Fault {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Fault{}, e.faults...)
}

// Apply finds the first matching, active fault for method+path.
func (e *Engine) Apply(method, path string) Hit {
	if e == nil {
		return Hit{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.total++
	for i := range e.faults {
		f := &e.faults[i]
		if !match(f, method, path) {
			continue
		}
		e.seen[i]++
		start := f.After
		if start <= 0 {
			start = 1
		}
		if e.seen[i] < start {
			continue
		}
		if f.Times > 0 && e.applied[i] >= f.Times {
			continue
		}
		e.applied[i]++
		h := Hit{Fault: f, Drift: f.Drift, Truncate: f.TruncateBytes}
		if f.Delay != "" {
			if d, err := time.ParseDuration(f.Delay); err == nil {
				h.Delay = d
			}
		}
		if f.Status > 0 || f.Malformed {
			h.Skip = true
		}
		return h
	}
	return Hit{}
}

func match(f *Fault, method, path string) bool {
	if f.Method != "" && !strings.EqualFold(f.Method, method) {
		return false
	}
	if f.PathPrefix != "" && !strings.HasPrefix(path, f.PathPrefix) {
		return false
	}
	if f.PathContains != "" && !strings.Contains(path, f.PathContains) {
		return false
	}
	return true
}

// BodyBytes renders the fault body.
func BodyBytes(f *Fault) []byte {
	if f == nil {
		return nil
	}
	if f.Malformed {
		return []byte(`{"errorMessages":["issuetap malformed`)
	}
	if f.Body == nil {
		if f.Status == 401 || f.Status == 403 {
			return []byte(`{"errorMessages":["Client must be authenticated to access this resource."],"errors":{}}`)
		}
		if f.Status == 429 {
			return []byte(`{"errorMessages":["Rate limit exceeded"],"errors":{}}`)
		}
		if f.Status >= 500 {
			return []byte(`{"errorMessages":["Internal server error"],"errors":{}}`)
		}
		return []byte(`{"errorMessages":["issuetap fault"],"errors":{}}`)
	}
	switch t := f.Body.(type) {
	case string:
		return []byte(t)
	case []byte:
		return t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return []byte(`{"errorMessages":["issuetap fault"]}`)
		}
		return b
	}
}

// Stats is dashboard telemetry.
func (e *Engine) Stats() []map[string]any {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]map[string]any, 0, len(e.faults))
	for i, f := range e.faults {
		out = append(out, map[string]any{
			"name":    f.Name,
			"status":  f.Status,
			"seen":    e.seen[i],
			"applied": e.applied[i],
		})
	}
	return out
}
