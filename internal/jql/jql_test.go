package jql

import (
	"testing"

	"github.com/midagedev/issuetap/internal/model"
)

func TestParseOrderOnly(t *testing.T) {
	q, err := Parse("ORDER BY updated DESC")
	if err != nil {
		t.Fatal(err)
	}
	if q.Root != nil {
		t.Fatal("expected match-all")
	}
	if len(q.Order) != 1 || q.Order[0].Field != "updated" || !q.Order[0].Desc {
		t.Fatalf("order=%v", q.Order)
	}
}

func TestParseProjectAndUpdated(t *testing.T) {
	q, err := Parse(`project in ("TAP", "OPS") AND updated >= "2026/08/01 10:00" ORDER BY updated ASC`)
	if err != nil {
		t.Fatal(err)
	}
	if q.Root == nil {
		t.Fatal("nil root")
	}
}

func TestParseBad(t *testing.T) {
	if _, err := Parse("%%%"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFilterKey(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Updated: "2026-08-02T10:00:00.000+0900"},
		{Key: "OPS-1", ProjectKey: "OPS", Updated: "2026-08-03T10:00:00.000+0900"},
	}
	q, err := Parse(`key = "TAP-1"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("%v", got)
	}
}

func TestFilterProjectIn(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Updated: "2026-08-02T10:00:00.000+0900"},
		{Key: "OPS-1", ProjectKey: "OPS", Updated: "2026-08-03T10:00:00.000+0900"},
	}
	q, err := Parse(`project in ("OPS")`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "OPS-1" {
		t.Fatalf("%v", got)
	}
}

func TestFilterFixVersion(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", FixVersions: []model.Named{{ID: "v1", Name: "2026.8"}}},
		{Key: "TAP-2", ProjectKey: "TAP"},
	}
	q, err := Parse(`fixVersion = "2026.8"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("fixVersion by name: %v", got)
	}
	q, err = Parse(`fixVersion = v1`)
	if err != nil {
		t.Fatal(err)
	}
	got = Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("fixVersion by id: %v", got)
	}
}

func TestFilterComponent(t *testing.T) {
	issues := []*model.Issue{
		{Key: "TAP-1", ProjectKey: "TAP", Components: []model.Named{{ID: "c1", Name: "Core"}}},
		{Key: "TAP-2", ProjectKey: "TAP"},
	}
	q, err := Parse(`component = "Core"`)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter(issues, q, Lookup{}, 0, -1)
	if len(got) != 1 || got[0].Key != "TAP-1" {
		t.Fatalf("%v", got)
	}
}
