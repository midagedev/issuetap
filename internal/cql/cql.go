// Package cql evaluates the Confluence CQL subset gadak sends.
//
//	space="KEY" AND type=page AND lastModified >= "2006-01-02 15:04" ORDER BY lastmodified ASC
//	space="KEY" AND type=comment AND lastModified >= "..." order by lastmodified asc
//
// Unparseable CQL is an error. Returning every page would look like a working
// wiki and hide a client bug.
package cql

import (
	"fmt"
	"strings"
	"time"

	"github.com/midagedev/issuetap/internal/model"
)

// Query is a parsed CQL.
type Query struct {
	Space    string
	Type     string // page | comment | "" (any)
	After    time.Time
	HasAfter bool
	OrderAsc bool
	Raw      string
}

// Parse understands the gadak clauses above. Extra AND clauses that we do
// not implement are an error (honest, not silently dropped).
func Parse(raw string) (Query, error) {
	q := Query{Raw: raw, OrderAsc: true}
	s := strings.TrimSpace(raw)
	if s == "" {
		return q, fmt.Errorf("cql: empty query")
	}
	// Peel order by.
	low := strings.ToLower(s)
	if i := strings.Index(low, "order by"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if s == "" {
		return q, nil
	}
	parts := splitAND(s)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		field, op, val, err := splitPred(part)
		if err != nil {
			return q, err
		}
		switch strings.ToLower(field) {
		case "space":
			q.Space = unquote(val)
		case "type":
			q.Type = strings.ToLower(unquote(val))
		case "lastmodified":
			t, err := parseCQLTime(unquote(val))
			if err != nil {
				return q, err
			}
			q.After = t
			q.HasAfter = true
			_ = op
		default:
			return q, fmt.Errorf("cql: unsupported clause %q", part)
		}
	}
	return q, nil
}

// MatchPage reports whether a page satisfies a type=page (or untyped) query.
func MatchPage(q Query, p *model.Page) bool {
	if q.Type != "" && q.Type != "page" {
		return false
	}
	if q.Space != "" && !strings.EqualFold(p.SpaceKey, q.Space) {
		return false
	}
	if q.HasAfter {
		t, ok := parseWhen(p.When)
		if !ok || t.Before(q.After) {
			return false
		}
	}
	return true
}

// MatchComment reports whether a comment satisfies a type=comment query.
func MatchComment(q Query, spaceKey, when string) bool {
	if q.Type != "" && q.Type != "comment" {
		return false
	}
	if q.Space != "" && !strings.EqualFold(spaceKey, q.Space) {
		return false
	}
	if q.HasAfter {
		t, ok := parseWhen(when)
		if !ok || t.Before(q.After) {
			return false
		}
	}
	return true
}

func splitAND(s string) []string {
	low := strings.ToLower(s)
	var parts []string
	start := 0
	for {
		i := indexWord(low[start:], "and")
		if i < 0 {
			parts = append(parts, s[start:])
			return parts
		}
		parts = append(parts, s[start:start+i])
		start = start + i + 3
	}
}

func indexWord(s, word string) int {
	for i := 0; i+len(word) <= len(s); i++ {
		if s[i:i+len(word)] != word {
			continue
		}
		leftOK := i == 0 || !isIdent(s[i-1])
		rightOK := i+len(word) == len(s) || !isIdent(s[i+len(word)])
		if leftOK && rightOK {
			return i
		}
	}
	return -1
}

func isIdent(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

func splitPred(s string) (field, op, val string, err error) {
	ops := []string{">=", "<=", "!=", "=", ">", "<"}
	low := s
	for _, o := range ops {
		if i := strings.Index(low, o); i >= 0 {
			return strings.TrimSpace(s[:i]), o, strings.TrimSpace(s[i+len(o):]), nil
		}
	}
	return "", "", "", fmt.Errorf("cql: not a predicate: %q", s)
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func parseCQLTime(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339,
		model.JiraTime,
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cql: bad time %q", s)
}

func parseWhen(s string) (time.Time, bool) {
	t, err := parseCQLTime(s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
