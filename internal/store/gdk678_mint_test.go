package store

import "testing"

// gadak GDK-678: an add {"name": X} must mint the version/component even
// when the project catalog is non-empty. Before 2026-08-23 the mint only
// happened on an empty catalog, so the first version could be created by
// name but the second could not — measured live over the REST surface
// (STD-1 +v0.19 → 400 "unknown fixVersions" while v0.18 existed).
func TestNamedAddByNameMintsIntoNonEmptyCatalog(t *testing.T) {
	st := loadTiny(t)
	// tiny fixture: TAP-1 already carries a fixVersion, so the project
	// catalog is non-empty. A brand-new name must mint, not 400.
	err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"fixVersions": []any{
			map[string]any{"add": map[string]any{"name": "gdk678-brand-new"}},
		},
	}, "")
	if err != nil {
		t.Fatalf("add by new name into non-empty catalog: %v", err)
	}
	iss := st.Issue("TAP-2")
	if len(iss.FixVersions) != 1 || iss.FixVersions[0].Name != "gdk678-brand-new" {
		t.Fatalf("FixVersions=%+v, want the minted name", iss.FixVersions)
	}
	if iss.FixVersions[0].ID == "" {
		t.Fatal("minted version has no id")
	}
	// An unknown id is a pointer, not a mint request — still an error.
	if err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"fixVersions": []any{
			map[string]any{"add": map[string]any{"id": "99999"}},
		},
	}, ""); err == nil {
		t.Fatal("expected error for unknown fixVersions id")
	}
}
