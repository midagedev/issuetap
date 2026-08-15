package locale

import "testing"

func TestParse(t *testing.T) {
	if Parse("ko_KR") != KO {
		t.Fatal("ko_KR")
	}
	if Parse("en") != EN {
		t.Fatal("en")
	}
}

func TestStatusNameKO(t *testing.T) {
	if got := StatusName(KO, "3", "In Progress"); got != "진행 중" {
		t.Fatalf("got %q", got)
	}
}

func TestPriorityNameKO(t *testing.T) {
	if got := PriorityName(KO, "2", "High"); got != "높음" {
		t.Fatalf("got %q", got)
	}
}

func TestCategoryKeyNotLocalized(t *testing.T) {
	// The function localizes the name; the key is the caller's job to keep.
	if CategoryName(KO, "indeterminate") == "indeterminate" {
		t.Fatal("expected localized category name")
	}
}

func TestIssueTypeKO(t *testing.T) {
	if got := IssueTypeName(KO, "10003", "Task"); got != "작업" {
		t.Fatalf("got %q", got)
	}
}
