// Package adf builds and flattens Atlassian Document Format bodies.
package adf

import (
	"encoding/json"
	"strings"
)

// Doc wraps plain text in the ADF document gadak and Cloud expect.
func Doc(text string) json.RawMessage {
	if text == "" {
		return nil
	}
	n := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	}
	b, err := json.Marshal(n)
	if err != nil {
		return nil
	}
	return b
}

// Plain walks an ADF document (or a JSON string of one) and returns visible text.
// A wiki-markup / plain string body (DC) is returned as-is.
func Plain(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var inner string
		if json.Unmarshal(raw, &inner) == nil {
			if json.Valid([]byte(inner)) {
				return Plain(json.RawMessage(inner))
			}
			return inner
		}
	}
	if s[0] != '{' && s[0] != '[' {
		return s
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return s
	}
	var b strings.Builder
	walk(node, &b)
	return strings.TrimSpace(b.String())
}

func walk(n any, b *strings.Builder) {
	switch v := n.(type) {
	case map[string]any:
		if t, _ := v["type"].(string); t == "text" {
			if text, ok := v["text"].(string); ok {
				if b.Len() > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(text)
			}
		}
		if c, ok := v["content"].([]any); ok {
			for _, child := range c {
				walk(child, b)
			}
		}
	case []any:
		for _, child := range v {
			walk(child, b)
		}
	}
}

// StorageXHTML is a DC Confluence body.storage value.
func StorageXHTML(text string) string {
	if text == "" {
		return ""
	}
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
	return "<p>" + esc + "</p>"
}

// WikiMarkup is a DC Jira string body. ADF is flattened; already-plain is kept.
func WikiMarkup(raw json.RawMessage, fallback string) string {
	if p := Plain(raw); p != "" {
		return p
	}
	return fallback
}
