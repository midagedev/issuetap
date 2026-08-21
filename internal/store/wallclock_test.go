package store

import (
	"testing"
	"time"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/model"
)

// gadak GDK-369: a standalone workspace is someone's real tracker, so records
// must carry wall time, not the deterministic seed clock (whose January start
// reads as a sync bug). Applying a fixture must not drag a wall-clock store
// back to seed time either.
func TestWallClockStampsRealTime(t *testing.T) {
	s := New(Options{WallClock: true})
	if err := s.Apply(fixtures.Doc{
		Seed:     7,
		Projects: []fixtures.Project{{ID: "10000", Key: "STD", Name: "Standalone"}},
	}); err != nil {
		t.Fatal(err)
	}
	iss, err := s.CreateIssue(map[string]any{
		"project": map[string]any{"key": "STD"},
		"summary": "wall time probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := time.Parse(model.JiraTime, iss.Created)
	if err != nil {
		t.Fatalf("created %q: %v", iss.Created, err)
	}
	if d := time.Since(created); d < -time.Minute || d > time.Minute {
		t.Fatalf("created %s is %s from wall time — seed clock leaked through", iss.Created, d)
	}

	// Determinism default unchanged: without the option, the seed clock rules.
	det := New(Options{})
	if err := det.Apply(fixtures.Doc{Projects: []fixtures.Project{{ID: "10000", Key: "STD", Name: "Standalone"}}}); err != nil {
		t.Fatal(err)
	}
	iss2, err := det.CreateIssue(map[string]any{
		"project": map[string]any{"key": "STD"},
		"summary": "seed probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := time.Parse(model.JiraTime, iss2.Created)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Year() != 2026 || c2.Month() != time.January {
		t.Fatalf("seeded store stamped %s — determinism contract broke", iss2.Created)
	}
}
