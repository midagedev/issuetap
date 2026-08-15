// Package dialect selects Cloud v3 vs Data Center v2 request shapes.
//
// A dialect built from published documentation is a model, not the product.
// Cloud v3 is the v0 requirement (gadak's call set). DC is the read path so a
// client can be developed against it; it does not make a DC integration
// verified. See docs/COMPATIBILITY.md.
package dialect

import "strings"

// Kind is cloud or dc.
type Kind string

const (
	Cloud Kind = "cloud"
	DC    Kind = "dc"
)

// Config is the active dialect.
type Config struct {
	Kind        Kind
	ContextPath string // DC only, e.g. "/jira". Empty means host root.
}

// Parse accepts cloud, v3, dc, datacenter, server, v2.
func Parse(s string) Kind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dc", "datacenter", "data-center", "server", "v2":
		return DC
	default:
		return Cloud
	}
}

// NormalizeContext ensures a leading slash and no trailing slash.
func NormalizeContext(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

// JiraPrefix is /rest/api/3 or /<ctx>/rest/api/2.
func (c Config) JiraPrefix() string {
	if c.Kind == DC {
		return NormalizeContext(c.ContextPath) + "/rest/api/2"
	}
	return "/rest/api/3"
}

// WikiPrefix is /wiki/rest/api or /<ctx>/rest/api.
func (c Config) WikiPrefix() string {
	if c.Kind == DC {
		return NormalizeContext(c.ContextPath) + "/rest/api"
	}
	return "/wiki/rest/api"
}

// UsesADF is true for Cloud issue/page bodies.
func (c Config) UsesADF() bool {
	return c.Kind != DC
}

// AuthScheme is Basic (Cloud) or Bearer (DC PAT).
func (c Config) AuthScheme() string {
	if c.Kind == DC {
		return "Bearer"
	}
	return "Basic"
}

// IdentityKind is accountId or username/userKey.
func (c Config) IdentityKind() string {
	if c.Kind == DC {
		return "username"
	}
	return "accountId"
}
