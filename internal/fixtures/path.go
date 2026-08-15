package fixtures

import (
	"path/filepath"
	"runtime"
)

// RepoRoot is the issuetap module root (the directory that contains go.mod).
// Resolved from this file via runtime.Caller so tests do not depend on cwd
// or a developer laptop path.
func RepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// this file lives at internal/fixtures/path.go
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Example is examples/fixtures/<name> under the module root.
func Example(name string) string {
	return filepath.Join(RepoRoot(), "examples", "fixtures", name)
}
