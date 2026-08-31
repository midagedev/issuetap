package store

// gadak GDK-1208: PUT /issue field edits must land in the changelog the
// way transition and PUT /assignee already do — one group per request,
// one item per changed field, nothing for a field that did not change.

import (
	"testing"

	"github.com/midagedev/issuetap/internal/model"
)

func lastHistory(t *testing.T, st *Store, key string) model.History {
	t.Helper()
	iss := st.Issue(key)
	if len(iss.Histories) == 0 {
		t.Fatalf("%s has no histories", key)
	}
	return iss.Histories[len(iss.Histories)-1]
}

func historyItem(t *testing.T, h model.History, field string) model.HistoryItem {
	t.Helper()
	for _, it := range h.Items {
		if it.FieldID == field {
			return it
		}
	}
	t.Fatalf("no %s item in %+v", field, h.Items)
	return model.HistoryItem{}
}

func TestUpdateIssueRecordsChangelog(t *testing.T) {
	st := loadTiny(t)
	before := len(st.Issue("TAP-2").Histories)
	actor := "5b10a2844c20165700ede22g"
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"summary":  "renamed by PUT",
		"priority": map[string]any{"id": "1"},
	}, nil, actor); err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if len(iss.Histories) != before+1 {
		t.Fatalf("histories %d, want %d (one group per PUT)", len(iss.Histories), before+1)
	}
	h := lastHistory(t, st, "TAP-2")
	if h.Author.AccountID != actor {
		t.Fatalf("author %q, want %q", h.Author.AccountID, actor)
	}
	if h.Created != iss.Updated {
		t.Fatalf("history created %q, want issue updated %q", h.Created, iss.Updated)
	}
	sum := historyItem(t, h, "summary")
	if sum.FromString != "Add keyboard shortcut" || sum.ToString != "renamed by PUT" {
		t.Fatalf("summary item %+v", sum)
	}
	prio := historyItem(t, h, "priority")
	if prio.To != "1" || prio.ToString != "Highest" {
		t.Fatalf("priority item %+v", prio)
	}
}

func TestUpdateIssueAssigneeParityWithPutAssignee(t *testing.T) {
	st := loadTiny(t)
	actor := "5b10a2844c20165700ede21g"
	target := "5b10a2844c20165700ede22g"
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"assignee": map[string]any{"accountId": target},
	}, nil, actor); err != nil {
		t.Fatal(err)
	}
	h := lastHistory(t, st, "TAP-2")
	it := historyItem(t, h, "assignee")
	if it.To != target {
		t.Fatalf("assignee item %+v", it)
	}
	if it.ToString == "" || it.ToString == target {
		t.Fatalf("assignee ToString %q, want a display name", it.ToString)
	}
}

func TestUpdateIssueLabelOpsRecorded(t *testing.T) {
	st := loadTiny(t)
	before := len(st.Issue("TAP-2").Histories)
	if err := st.UpdateIssue("TAP-2", nil, map[string]any{
		"labels": []any{map[string]any{"add": "hotkey"}},
	}, ""); err != nil {
		t.Fatal(err)
	}
	if len(st.Issue("TAP-2").Histories) != before+1 {
		t.Fatal("update.labels add left no changelog group")
	}
	it := historyItem(t, lastHistory(t, st, "TAP-2"), "labels")
	if it.ToString != "hotkey" {
		t.Fatalf("labels item %+v", it)
	}
}

func TestUpdateIssueNoChangeNoChangelog(t *testing.T) {
	st := loadTiny(t)
	iss := st.Issue("TAP-2")
	before := len(iss.Histories)
	if err := st.UpdateIssue("TAP-2", map[string]any{
		"summary": iss.Summary,
	}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if got := len(st.Issue("TAP-2").Histories); got != before {
		t.Fatalf("histories %d, want %d (no-change writes record nothing)", got, before)
	}
}

func TestSetAssigneeNoChangeNoChangelog(t *testing.T) {
	st := loadTiny(t)
	cur := st.Issue("TAP-1").AssigneeID
	if cur == "" {
		t.Fatal("fixture TAP-1 should be assigned")
	}
	before := len(st.Issue("TAP-1").Histories)
	if err := st.SetAssignee("TAP-1", cur, cur); err != nil {
		t.Fatal(err)
	}
	if got := len(st.Issue("TAP-1").Histories); got != before {
		t.Fatalf("histories %d, want %d (same assignee records nothing)", got, before)
	}
}
