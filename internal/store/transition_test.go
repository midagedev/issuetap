package store

import (
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

func transitionTo(t *testing.T, st *Store, key, statusID string) string {
	t.Helper()
	for _, tr := range st.Transitions(key) {
		if tr.ToID == statusID {
			return tr.ID
		}
	}
	t.Fatalf("no transition from %s to status %s", key, statusID)
	return ""
}

func applyDoneResolutionScreen(t *testing.T, st *Store, src string) {
	t.Helper()
	doc, err := fixtures.Load(fixtures.Example(src))
	if err != nil {
		t.Fatal(err)
	}
	doc.TransitionScreens = []fixtures.TransitionScreen{{
		Status: "10003",
		Fields: map[string]fixtures.TransitionScreenField{
			"resolution": {Required: true},
		},
	}}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTransitionStoresRequestedResolution(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	applyDoneResolutionScreen(t, st, "tiny.yaml")
	id := transitionTo(t, st, "TAP-1", "10003")
	err := st.ApplyTransition("TAP-1", id, "", map[string]any{
		"resolution": map[string]any{"id": "10002"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if iss.StatusID != "10003" {
		t.Fatalf("status=%s, want 10003", iss.StatusID)
	}
	if iss.ResolutionID != "10002" {
		t.Fatalf("resolution=%s, want 10002", iss.ResolutionID)
	}
}

func TestApplyTransitionRequiresResolutionWhenScreenSaysSo(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	applyDoneResolutionScreen(t, st, "tiny.yaml")
	id := transitionTo(t, st, "TAP-1", "10003")
	err := st.ApplyTransition("TAP-1", id, "", nil, nil)
	fe, ok := AsFieldError(err)
	if !ok || fe.Map()["resolution"] == "" {
		t.Fatalf("want FieldError.resolution, got %T %v", err, err)
	}
	iss := st.Issue("TAP-1")
	if iss.StatusID != "3" {
		t.Fatalf("status moved on required miss: %s", iss.StatusID)
	}
}

func TestApplyTransitionRejectsFieldsWithoutScreen(t *testing.T) {
	st := loadTiny(t)
	id := transitionTo(t, st, "TAP-1", "10003")
	err := st.ApplyTransition("TAP-1", id, "", map[string]any{
		"resolution": map[string]any{"id": "10002"},
	}, nil)
	fe, ok := AsFieldError(err)
	if !ok || fe.Map()["resolution"] == "" {
		t.Fatalf("want FieldError.resolution, got %T %v", err, err)
	}
}

func TestApplyTransitionRejectsUnknownResolutionID(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	applyDoneResolutionScreen(t, st, "tiny.yaml")
	id := transitionTo(t, st, "TAP-1", "10003")
	err := st.ApplyTransition("TAP-1", id, "", map[string]any{
		"resolution": map[string]any{"id": "99999"},
	}, nil)
	fe, ok := AsFieldError(err)
	if !ok || fe.Map()["resolution"] == "" {
		t.Fatalf("want FieldError.resolution, got %T %v", err, err)
	}
}

func TestApplyTransitionDefaultsDoneResolutionWhenOmitted(t *testing.T) {
	st := loadTiny(t)
	id := transitionTo(t, st, "TAP-1", "10003")
	if err := st.Transition("TAP-1", id); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if iss.ResolutionID != "10000" {
		t.Fatalf("resolution=%s, want 10000", iss.ResolutionID)
	}
}

func TestApplyTransitionClearsResolutionWhenLeavingDone(t *testing.T) {
	st := loadTiny(t)
	if st.Issue("TAP-3").ResolutionID != "10000" {
		t.Fatalf("precondition TAP-3 resolution=%s", st.Issue("TAP-3").ResolutionID)
	}
	id := transitionTo(t, st, "TAP-3", "10000")
	if err := st.Transition("TAP-3", id); err != nil {
		t.Fatal(err)
	}
	if st.Issue("TAP-3").ResolutionID != "" {
		t.Fatalf("leaving done left resolution=%s", st.Issue("TAP-3").ResolutionID)
	}
}

func TestApplyTransitionKeysResolutionByIDNotName(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.KO})
	applyDoneResolutionScreen(t, st, "korean.yaml")
	st.SetLocale(locale.KO)
	id := transitionTo(t, st, "TAP-1", "10003")
	err := st.ApplyTransition("TAP-1", id, "", map[string]any{
		"resolution": map[string]any{"name": "Duplicate"},
	}, nil)
	if _, ok := AsFieldError(err); !ok {
		t.Fatalf("name-only payload must not match, got %v", err)
	}
	err = st.ApplyTransition("TAP-1", id, "", map[string]any{
		"resolution": map[string]any{"id": "10002"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Issue("TAP-1").ResolutionID != "10002" {
		t.Fatalf("resolution=%s, want 10002", st.Issue("TAP-1").ResolutionID)
	}
}

func TestTransitionScreensSurviveSnapshot(t *testing.T) {
	st := New(Options{Seed: 1, Locale: locale.EN})
	applyDoneResolutionScreen(t, st, "tiny.yaml")
	doc := st.Snapshot()
	st2 := New(Options{Seed: 1, Locale: locale.EN})
	if err := st2.Apply(doc); err != nil {
		t.Fatal(err)
	}
	id := transitionTo(t, st2, "TAP-1", "10003")
	err := st2.ApplyTransition("TAP-1", id, "", nil, nil)
	if _, ok := AsFieldError(err); !ok {
		t.Fatalf("snapshot dropped the required screen: %v", err)
	}
}

func TestTransitionScreenFieldsEmptyObjectWithoutScreen(t *testing.T) {
	st := loadTiny(t)
	fields := st.TransitionScreenFields("10003")
	if fields == nil {
		t.Fatal("fields must be {}, not nil")
	}
	if len(fields) != 0 {
		t.Fatalf("fields=%v, want {}", fields)
	}
}
