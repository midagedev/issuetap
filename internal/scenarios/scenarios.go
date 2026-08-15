// Package scenarios layers faults, locale, and dialect on a fixture and
// produces a machine-readable report.
package scenarios

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/midagedev/issuetap/internal/faults"
)

// Scenario is the on-disk document.
type Scenario struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Fixture     string         `json:"fixture,omitempty" yaml:"fixture,omitempty"`
	Dialect     string         `json:"dialect,omitempty" yaml:"dialect,omitempty"`
	Locale      string         `json:"locale,omitempty" yaml:"locale,omitempty"`
	Seed        int64          `json:"seed,omitempty" yaml:"seed,omitempty"`
	ContextPath string         `json:"contextPath,omitempty" yaml:"contextPath,omitempty"`
	Faults      []faults.Fault `json:"faults,omitempty" yaml:"faults,omitempty"`
	// Assertions are HTTP checks the runner performs against a live server.
	Assertions []Assertion `json:"assertions,omitempty" yaml:"assertions,omitempty"`
}

// Assertion is one HTTP check.
type Assertion struct {
	Name           string            `json:"name" yaml:"name"`
	Method         string            `json:"method,omitempty" yaml:"method,omitempty"`
	Path           string            `json:"path" yaml:"path"`
	Body           any               `json:"body,omitempty" yaml:"body,omitempty"`
	Headers        map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	WantStatus     int               `json:"wantStatus,omitempty" yaml:"wantStatus,omitempty"`
	WantContains   string            `json:"wantContains,omitempty" yaml:"wantContains,omitempty"`
	WantJSONPath   string            `json:"wantJSONPath,omitempty" yaml:"wantJSONPath,omitempty"` // dotted, e.g. issuetap.code
	WantJSONValue  string            `json:"wantJSONValue,omitempty" yaml:"wantJSONValue,omitempty"`
	Auth           bool              `json:"auth,omitempty" yaml:"auth,omitempty"` // send Basic you@example.com:token
}

// Report is the runner output.
type Report struct {
	Name    string           `json:"name"`
	Passed  bool             `json:"passed"`
	Results []AssertionResult `json:"results"`
}

// AssertionResult is one check.
type AssertionResult struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Got      string `json:"got,omitempty"`
	Want     string `json:"want,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Load reads a scenario file.
func Load(path string) (Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	sc, err := Parse(b)
	if err != nil {
		return sc, err
	}
	if sc.Fixture != "" && !filepath.IsAbs(sc.Fixture) {
		sc.Fixture = filepath.Join(filepath.Dir(path), sc.Fixture)
	}
	return sc, nil
}

// Parse decodes YAML or JSON.
func Parse(b []byte) (Scenario, error) {
	b = bytes.TrimSpace(b)
	var sc Scenario
	var err error
	if len(b) > 0 && b[0] == '{' {
		err = json.Unmarshal(b, &sc)
	} else {
		err = yaml.Unmarshal(b, &sc)
	}
	if err != nil {
		return sc, fmt.Errorf("scenario: %w", err)
	}
	if sc.Name == "" {
		return sc, fmt.Errorf("scenario: name is required")
	}
	return sc, nil
}

// MarshalJSON report.
func (r Report) Marshal() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return b
}

// FailCount is the number of failed assertions.
func (r Report) FailCount() int {
	n := 0
	for _, x := range r.Results {
		if !x.Passed {
			n++
		}
	}
	return n
}

// RelativizeFixture keeps a fixture path next to the scenario when possible.
func RelativizeFixture(scDir, fixture string) string {
	rel, err := filepath.Rel(scDir, fixture)
	if err != nil {
		return fixture
	}
	if strings.HasPrefix(rel, "..") {
		return fixture
	}
	return rel
}
