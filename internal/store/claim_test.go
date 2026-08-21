package store

// Claim contract (gadak GDK-591): a claim is one atomic mutation —
// assignee + in-progress transition under s.mu — so concurrent claims by
// two agents have exactly one winner; another actor's hold is a conflict
// naming the holder; the same actor's re-claim is idempotent (no duplicate
// changelog); takeOver reassigns without a re-transition; the in-progress
// destination is chosen by statusCategory.key, never a localized name.
//
// | Contract | Test |
// | --- | --- |
// | concurrent claims: one winner, one conflict | TestClaimConcurrentTwoActorsOneWinner |
// | fresh claim writes status + assignee rows, author actor | TestClaimWritesStatusAndAssigneeRows |
// | another actor's hold is a conflict naming the holder | TestClaimConflictNamesHolder |
// | same actor re-claim is idempotent (changelog unchanged) | TestClaimSameActorIdempotent |
// | takeOver reassigns, leaves the takeover trace, no re-transition | TestClaimTakeOverLeavesTrace |
// | no in-progress destination is an error, issue untouched | TestClaimNoInProgressTransition |
// | ko locale: category keying, first in-progress destination, explicit id | TestClaimKoreanLocaleKeysCategoryNotName |

import (
	"strconv"
	"sync"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

const (
	danaAccountID         = "5b10a2844c20165700ede22g"     // tiny.yaml assignee of TAP-1
	tap1EnteredInProgress = "2026-07-20T10:00:00.000+0900" // tiny.yaml history h2
)

// claimableTAP2 asserts the precondition every fresh-claim test relies on:
// TAP-2 sits in To Do unassigned.
func claimableTAP2(t *testing.T, st *Store) {
	t.Helper()
	iss := st.Issue("TAP-2")
	if iss == nil {
		t.Fatal("TAP-2 missing")
	}
	if iss.StatusID != "10000" || iss.AssigneeID != "" {
		t.Fatalf("TAP-2 precondition: status=%s assignee=%q, want 10000/\"\"", iss.StatusID, iss.AssigneeID)
	}
}

func changelogRows(st *Store, key string) int {
	return len(st.Issue(key).Histories)
}

func TestClaimConcurrentTwoActorsOneWinner(t *testing.T) {
	st := loadTiny(t)
	claimableTAP2(t, st)

	const actors = 2
	start := make(chan struct{})
	results := make([]error, actors)
	var wg sync.WaitGroup
	for i := 0; i < actors; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = st.Claim("TAP-2", "agent:"+strconv.Itoa(i), "", false)
		}(i)
	}
	close(start)
	wg.Wait()

	wins, conflicts := 0, 0
	winner := ""
	for i, err := range results {
		switch {
		case err == nil:
			wins++
			winner = "agent:" + strconv.Itoa(i)
		case IsConflict(err):
			conflicts++
		default:
			t.Fatalf("agent %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d, want 1/1 (errors: %v)", wins, conflicts, results)
	}
	iss := st.Issue("TAP-2")
	if iss.AssigneeID != winner {
		t.Fatalf("assignee=%q, want the winner %q", iss.AssigneeID, winner)
	}
	if !st.issueInProgressLocked(iss) {
		t.Fatalf("status=%s, want an in-progress (indeterminate) status", iss.StatusID)
	}
	// The loser wrote nothing: exactly the winner's status + assignee rows.
	if got := changelogRows(st, "TAP-2"); got != 2 {
		t.Fatalf("changelog rows=%d, want 2 (status + assignee), loser must not write", got)
	}
}

func TestClaimWritesStatusAndAssigneeRows(t *testing.T) {
	st := loadTiny(t)
	claimableTAP2(t, st)
	res, err := st.Claim("TAP-2", "claude:354bff2b", "", false)
	if err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-2")
	if iss.StatusID != "3" {
		t.Fatalf("status=%s, want 3 (In Progress)", iss.StatusID)
	}
	if iss.AssigneeID != "claude:354bff2b" {
		t.Fatalf("assignee=%q, want the actor", iss.AssigneeID)
	}
	hist := iss.Histories
	if len(hist) != 2 {
		t.Fatalf("changelog rows=%d, want status + assignee", len(hist))
	}
	for i, h := range hist {
		if h.Author.AccountID != "claude:354bff2b" {
			t.Fatalf("row %d author=%q, want the claiming actor", i, h.Author.AccountID)
		}
	}
	if hist[0].Items[0].FieldID != "status" || hist[1].Items[0].FieldID != "assignee" {
		t.Fatalf("row order=%v %v, want status then assignee", hist[0].Items[0], hist[1].Items[0])
	}
	if res.ClaimedAt != hist[0].Created {
		t.Fatalf("claimedAt=%q, want the status row's created %q", res.ClaimedAt, hist[0].Created)
	}
	if res.Key != "TAP-2" || res.Status.ID != "3" || res.Assignee.AccountID != "claude:354bff2b" {
		t.Fatalf("result=%+v", res)
	}
}

func TestClaimConflictNamesHolder(t *testing.T) {
	st := loadTiny(t)
	// TAP-1 is In Progress, assigned to Dana.
	_, err := st.Claim("TAP-1", "grok:tars", "", false)
	if !IsConflict(err) {
		t.Fatalf("error=%v, want a conflict", err)
	}
	want := "TAP-1 is already claimed by Dana"
	if err.Error() != want {
		t.Fatalf("message=%q, want %q", err.Error(), want)
	}
	// The rejection wrote nothing.
	if got := changelogRows(st, "TAP-1"); got != 3 {
		t.Fatalf("changelog rows=%d, want the 3 fixture rows untouched", got)
	}
}

func TestClaimSameActorIdempotent(t *testing.T) {
	st := loadTiny(t)
	before := changelogRows(st, "TAP-1")
	updatedBefore := st.Issue("TAP-1").Updated
	res, err := st.Claim("TAP-1", danaAccountID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := changelogRows(st, "TAP-1"); got != before {
		t.Fatalf("changelog rows=%d, want unchanged %d (no duplicate rows)", got, before)
	}
	if st.Issue("TAP-1").Updated != updatedBefore {
		t.Fatal("idempotent re-claim must not bump updated")
	}
	// claimedAt is the changelog's entry into the current status (h2), not now.
	if res.ClaimedAt != tap1EnteredInProgress {
		t.Fatalf("claimedAt=%q, want the changelog time %q", res.ClaimedAt, tap1EnteredInProgress)
	}
}

func TestClaimTakeOverLeavesTrace(t *testing.T) {
	st := loadTiny(t)
	before := changelogRows(st, "TAP-1")
	res, err := st.Claim("TAP-1", "claude:354bff2b", "", true)
	if err != nil {
		t.Fatal(err)
	}
	iss := st.Issue("TAP-1")
	if iss.AssigneeID != "claude:354bff2b" {
		t.Fatalf("assignee=%q, want the taking-over actor", iss.AssigneeID)
	}
	if iss.StatusID != "3" {
		t.Fatalf("status=%s, want 3 unchanged (takeover is not a re-transition)", iss.StatusID)
	}
	if got := changelogRows(st, "TAP-1"); got != before+1 {
		t.Fatalf("changelog rows=%d, want %d (one assignee row)", got, before+1)
	}
	last := iss.Histories[len(iss.Histories)-1]
	item := last.Items[0]
	if item.FieldID != "assignee" || item.From != danaAccountID || item.To != "claude:354bff2b" {
		t.Fatalf("takeover row=%v, want assignee Dana→claude", item)
	}
	if last.Author.AccountID != "claude:354bff2b" {
		t.Fatalf("takeover author=%q, want the taking-over actor", last.Author.AccountID)
	}
	if item.FromString != "Dana" {
		t.Fatalf("fromString=%q, want the holder's display name", item.FromString)
	}
	// The takeover does not move the in-progress clock: claimedAt stays the
	// changelog's entry into the current status. The takeover itself is the
	// new assignee row's own Created.
	if res.ClaimedAt != tap1EnteredInProgress {
		t.Fatalf("claimedAt=%q, want %q", res.ClaimedAt, tap1EnteredInProgress)
	}
}

func TestClaimNoInProgressTransition(t *testing.T) {
	st := loadTiny(t)
	// Overwrite the only in-progress status to new (putStatus upserts), so
	// TAP-2 has no indeterminate destination.
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	doc.Statuses = append(doc.Statuses, fixtures.Status{ID: "3", Name: "Blocked", Category: "new"})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	before := changelogRows(st, "TAP-2")
	_, err = st.Claim("TAP-2", "claude:354bff2b", "", false)
	if err == nil || err.Error() != "no in-progress transition available" {
		t.Fatalf("error=%v, want no in-progress transition available", err)
	}
	if IsNotFound(err) || IsConflict(err) {
		t.Fatalf("error=%v must not classify as not-found/conflict (it is a 400)", err)
	}
	iss := st.Issue("TAP-2")
	if iss.StatusID != "10000" || iss.AssigneeID != "" || changelogRows(st, "TAP-2") != before {
		t.Fatalf("failed claim mutated the issue: status=%s assignee=%q rows=%d", iss.StatusID, iss.AssigneeID, changelogRows(st, "TAP-2"))
	}
}

func TestClaimKoreanLocaleKeysCategoryNotName(t *testing.T) {
	doc, err := fixtures.Load(fixtures.Example("korean.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	st := New(Options{Seed: 1, Locale: locale.KO})
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	st.SetLocale(locale.KO)

	// TAP-2 sits in 해야 할 일 (10000). Destination ids sort as
	// [10002, 10003, 3], so the first in-progress destination is 검토 중
	// (10002) — chosen by category key, not by the name 진행 중.
	res, err := st.Claim("TAP-2", "claude:354bff2b", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.ID != "10002" || res.Status.StatusCategory.Key != "indeterminate" {
		t.Fatalf("status=%s (%s), want 10002 by category key", res.Status.ID, res.Status.StatusCategory.Key)
	}
	iss := st.Issue("TAP-2")
	if iss.StatusID != "10002" || iss.AssigneeID != "claude:354bff2b" {
		t.Fatalf("TAP-2 status=%s assignee=%q", iss.StatusID, iss.AssigneeID)
	}

	// An explicit transition id still wins: name the 진행 중 (3) transition.
	st2 := New(Options{Seed: 1, Locale: locale.KO})
	if err := st2.Apply(doc); err != nil {
		t.Fatal(err)
	}
	st2.SetLocale(locale.KO)
	id := transitionTo(t, st2, "TAP-2", "3")
	res, err = st2.Claim("TAP-2", "grok:tars", id, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.ID != "3" {
		t.Fatalf("explicit transition gave status=%s, want 3 (진행 중)", res.Status.ID)
	}
}
