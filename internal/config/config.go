// Package config is the process configuration. Nothing here is a secret
// that came from the maintainer's live site — accepted credentials are
// whatever the operator passed on the command line.
package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/dialect"
	"github.com/midagedev/issuetap/internal/locale"
)

// Config is serve-time settings.
type Config struct {
	Addr        string
	Fixture     string
	Locale      locale.Code
	Dialect     dialect.Config
	Seed        int64
	Email       string // accepted Basic user; empty = any
	Token       string // accepted Basic password / Bearer token; empty = any non-empty
	Snapshot    string // optional path to persist snapshot on shutdown
	PublicBase  string
}

// Default is 127.0.0.1:8080, Cloud, English, seed 1.
func Default() Config {
	return Config{
		Addr:    "127.0.0.1:8080",
		Locale:  locale.EN,
		Dialect: dialect.Config{Kind: dialect.Cloud},
		Seed:    1,
	}
}

// FromEnv overlays ISSUETAP_* variables. Values are never logged here.
func FromEnv(c Config) Config {
	if v := os.Getenv("ISSUETAP_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("ISSUETAP_FIXTURE"); v != "" {
		c.Fixture = v
	}
	if v := os.Getenv("ISSUETAP_LOCALE"); v != "" {
		c.Locale = locale.Parse(v)
	}
	if v := os.Getenv("ISSUETAP_DIALECT"); v != "" {
		c.Dialect.Kind = dialect.Parse(v)
	}
	if v := os.Getenv("ISSUETAP_CONTEXT_PATH"); v != "" {
		c.Dialect.ContextPath = dialect.NormalizeContext(v)
	}
	if v := os.Getenv("ISSUETAP_SEED"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Seed = n
		}
	}
	if v := os.Getenv("ISSUETAP_EMAIL"); v != "" {
		c.Email = v
	}
	if v := os.Getenv("ISSUETAP_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("ISSUETAP_SNAPSHOT"); v != "" {
		c.Snapshot = v
	}
	if v := os.Getenv("ISSUETAP_PUBLIC_BASE_PATH"); v != "" {
		c.PublicBase = v
	}
	return c
}

// Accepts reports whether the presented credential is allowed.
// Empty configured email/token means "any non-empty Authorization".
func (c Config) Accepts(scheme, user, secret string) bool {
	if secret == "" && user == "" {
		return false
	}
	if c.Dialect.Kind == dialect.DC {
		// DC: Bearer PAT. If Token is set, it must match. Email is ignored.
		if c.Token != "" {
			return secret == c.Token || user == c.Token
		}
		return secret != "" || user != ""
	}
	// Cloud: Basic email:token.
	if c.Email != "" && !strings.EqualFold(user, c.Email) {
		return false
	}
	if c.Token != "" && secret != c.Token {
		return false
	}
	return true
}
