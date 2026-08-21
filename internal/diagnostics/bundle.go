// Package diagnostics writes a machine-readable zip an agent or CI can read.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/store"
)

// Input is everything the bundle needs. Traces must already be redacted
// of Authorization headers (the server never copies the credential into a
// Trace).
type Input struct {
	Dialect  string
	Locale   string
	Seed     int64
	Counts   map[string]int
	Traces   []map[string]any
	Snapshot fixtures.Doc
	Faults   []map[string]any
	Routes   any
	Report   any
}

// Build returns a zip archive.
func Build(in Input) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, v any) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	meta := map[string]any{
		"product":   "issuetap",
		"createdAt": time.Now().UTC().Format(time.RFC3339),
		"dialect":   in.Dialect,
		"locale":    in.Locale,
		"seed":      in.Seed,
		"counts":    in.Counts,
	}
	if err := write("meta.json", meta); err != nil {
		return nil, err
	}
	if err := write("traces.json", in.Traces); err != nil {
		return nil, err
	}
	if err := write("snapshot.json", in.Snapshot); err != nil {
		return nil, err
	}
	if err := write("faults.json", in.Faults); err != nil {
		return nil, err
	}
	if err := write("compatibility.json", in.Routes); err != nil {
		return nil, err
	}
	if in.Report != nil {
		if err := write("scenario-report.json", in.Report); err != nil {
			return nil, err
		}
	}
	if err := write("diagnosis.json", diagnose(in)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func diagnose(in Input) map[string]any {
	var causes []string
	var next []string
	authHits := 0
	rateHits := 0
	unsup := 0
	for _, t := range in.Traces {
		st, _ := asInt(t["status"])
		switch {
		case st == 401 || st == 403:
			authHits++
		case st == 429:
			rateHits++
		case st == 501:
			unsup++
		}
	}
	if authHits > 0 {
		causes = append(causes, fmt.Sprintf("%d requests returned 401/403 — a revoked-credential scenario or a missing Authorization header", authHits))
		next = append(next, "Check the request log for the first 401. If a fault fired, the client should stop (gadak Watch does). If no fault fired, the client omitted Authorization.")
	}
	if rateHits > 0 {
		causes = append(causes, fmt.Sprintf("%d requests returned 429", rateHits))
		next = append(next, "Confirm the client honours Retry-After. gadak retries 429 with that delay.")
	}
	if unsup > 0 {
		causes = append(causes, fmt.Sprintf("%d requests hit unsupported_endpoint (HTTP 501)", unsup))
		next = append(next, "Read issuetap.code in the body. The route is known but not in the v0 surface — see docs/COMPATIBILITY.md. Do not treat this as a client bug.")
	}
	if in.Counts["issues"] == 0 {
		causes = append(causes, "the graph has zero issues")
		next = append(next, "Apply a fixture: issuetap fixtures apply examples/fixtures/tiny.yaml")
	}
	if n := store.InvalidParentCount(in.Snapshot); n > 0 {
		causes = append(causes, fmt.Sprintf("%d issue parent links violate hierarchy: parent must exist and be exactly one hierarchyLevel above the child", n))
		next = append(next, "Inspect snapshot.json issues[].parent against issueTypes[].hierarchyLevel. Load keeps the links; POST/PUT /issue rejects new ones.")
	}
	if len(causes) == 0 {
		causes = append(causes, "no automatic diagnosis — inspect traces.json and snapshot.json")
		next = append(next, "Compare the last request body to the documented shape for that path.")
	}
	return map[string]any{
		"likelyCauses": causes,
		"nextChecks":   next,
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	}
	return 0, false
}
