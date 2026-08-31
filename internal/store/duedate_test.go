package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

// TestUpdateIssueDuedateIsFirstClass is FAIL-first for GDK-324:
// before the dedicated case, UpdateIssue dumped "duedate" into
// Issue.Custom and left Issue.Duedate untouched.
func TestUpdateIssueDuedateIsFirstClass(t *testing.T) {
	st := loadTiny(t)
	if err := st.UpdateIssue("TAP-2", map[string]any{"duedate": "2026-09-01"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if iss == nil {
		t.Fatal("TAP-2 missing")
	}
	if _, inCustom := iss.Custom["duedate"]; inCustom {
		t.Fatalf("duedate stored in Custom (%v); want first-class Issue.Duedate", iss.Custom["duedate"])
	}
	if iss.Duedate != "2026-09-01" {
		t.Fatalf("Duedate=%q, want 2026-09-01", iss.Duedate)
	}
}

func TestUpdateIssueDuedateRejectsBadFormat(t *testing.T) {
	st := loadTiny(t)
	err := st.UpdateIssue("TAP-2", map[string]any{"duedate": "09/01/2026"}, nil, "")
	if err == nil {
		t.Fatal("expected error for non YYYY-MM-DD duedate")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "duedate" {
		t.Fatalf("want FieldError{Field:duedate}, got %T %v", err, err)
	}
	iss := st.Issue("TAP-2")
	if iss.Duedate != "" {
		t.Fatalf("rejected write mutated Duedate=%q", iss.Duedate)
	}
	if _, inCustom := iss.Custom["duedate"]; inCustom {
		t.Fatal("rejected write still landed in Custom")
	}
}

func TestUpdateIssueDuedateClearsOnNull(t *testing.T) {
	st := loadTiny(t)
	if err := st.UpdateIssue("TAP-1", map[string]any{"duedate": nil}, nil, ""); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if iss.Duedate != "" {
		t.Fatalf("Duedate=%q after null, want empty", iss.Duedate)
	}
}

func TestCreateIssueDuedateIsFirstClass(t *testing.T) {
	st := loadTiny(t)
	iss, err := st.CreateIssue(map[string]any{
		"project": map[string]any{"key": "TAP"},
		"summary": "has a due date",
		"duedate": "2026-10-15",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, inCustom := iss.Custom["duedate"]; inCustom {
		t.Fatal("CreateIssue stored duedate in Custom")
	}
	if iss.Duedate != "2026-10-15" {
		t.Fatalf("Duedate=%q", iss.Duedate)
	}
}

func TestRegisteredOptionRejectsUnknownID(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Issues:   []fixtures.Issue{{Key: "TAP-1", Summary: "x", Project: "TAP"}},
		Fields: []fixtures.Field{{
			ID: "customfield_10050", Name: "Severity", Custom: true, Type: "option",
			Options: []fixtures.FieldOption{
				{ID: "10100", Value: "Sev1"},
				{ID: "10101", Value: "Sev2"},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	err := st.UpdateIssue("TAP-1", map[string]any{
		"customfield_10050": map[string]any{"id": "99999"},
	}, nil, "")
	if err == nil {
		t.Fatal("expected 400-class error for unknown option id")
	}
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "customfield_10050" {
		t.Fatalf("want FieldError{Field:customfield_10050}, got %T %v", err, err)
	}
}

func TestRegisteredOptionAcceptsKnownID(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Issues:   []fixtures.Issue{{Key: "TAP-1", Summary: "x", Project: "TAP"}},
		Fields: []fixtures.Field{{
			ID: "customfield_10050", Name: "Severity", Custom: true, Type: "option",
			Options: []fixtures.FieldOption{{ID: "10100", Value: "Sev1"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateIssue("TAP-1", map[string]any{
		"customfield_10050": map[string]any{"id": "10100"},
	}, nil, ""); err != nil {
		t.Fatal(err)
	}
	got := st.Issue("TAP-1").Custom["customfield_10050"]
	m, _ := got.(map[string]any)
	if m["id"] != "10100" {
		t.Fatalf("stored %v", got)
	}
}

// gadak GDK-1207: an unregistered field is a FieldError, never a free
// write into Custom.
func TestUnregisteredCustomFieldRejected(t *testing.T) {
	st := loadTiny(t)
	err := st.UpdateIssue("TAP-2", map[string]any{
		"customfield_99999": map[string]any{"value": "whatever"},
	}, nil, "")
	fe, ok := AsFieldError(err)
	if !ok || fe.Field != "customfield_99999" {
		t.Fatalf("want FieldError{Field:customfield_99999}, got %T %v", err, err)
	}
	if st.Issue("TAP-2").Custom["customfield_99999"] != nil {
		t.Fatal("rejected write still landed in Custom")
	}
}

func TestCustomFieldRegistrySurvivesSnapshot(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	if err := st.Apply(fixtures.Doc{
		Projects: []fixtures.Project{{Key: "TAP", Name: "Tap"}},
		Issues:   []fixtures.Issue{{Key: "TAP-1", Summary: "x", Project: "TAP"}},
		Fields: []fixtures.Field{{
			ID: "customfield_10050", Name: "Severity", Custom: true, Type: "option",
			Options: []fixtures.FieldOption{{ID: "10100", Value: "Sev1"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if len(snap.Fields) != 1 || snap.Fields[0].ID != "customfield_10050" {
		t.Fatalf("snapshot fields=%+v", snap.Fields)
	}
	if len(snap.Fields[0].Options) != 1 || snap.Fields[0].Options[0].ID != "10100" {
		t.Fatalf("snapshot options=%+v", snap.Fields[0].Options)
	}
	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(snap); err != nil {
		t.Fatal(err)
	}
	if err := st2.UpdateIssue("TAP-1", map[string]any{
		"customfield_10050": map[string]any{"id": "99999"},
	}, nil, ""); err == nil {
		t.Fatal("restored registry lost option validation")
	}
}
