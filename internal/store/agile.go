package store

// Agile API store methods (gadak GDK-1666, docs/decisions/0002). The HTTP
// half lives in internal/api/agile.go; this file is the graph side.
// Boards are created lazily — exactly one scrum board per project, minted
// in project-key order so ids are stable no matter which filter asked —
// and sprint state follows the two transitions Jira's own API defines
// (start requires dates, close sweeps incomplete issues to the backlog).
// Issue membership is model.Issue.SprintIDs; the tail is the current
// sprint, which is the only thing JQL membership reads.

import (
	"fmt"
	"strconv"

	"github.com/midagedev/issuetap/internal/clock"
	"github.com/midagedev/issuetap/internal/model"
)

// Boards materializes one scrum board per project (in project-key order,
// so ids do not depend on which filter ran first) and returns the ones
// matching projectKeyOrID ("" = all) and typ ("" or "scrum" = all; issuetap
// has no other kind).
func (s *Store) Boards(projectKeyOrID, typ string) []model.Board {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projectsLocked() {
		if s.boardByProjectLocked(p.Key) == nil {
			s.putBoardLocked(&model.Board{
				ID:   int64(s.nextSeqLocked("board")),
				Name: p.Key + " board", Type: "scrum", ProjectKey: p.Key,
			})
		}
	}
	out := []model.Board{}
	for _, b := range s.boardsLocked() {
		if projectKeyOrID != "" {
			p := s.projectByKeyLocked(projectKeyOrID)
			if p == nil {
				p = s.projectByIDLocked(projectKeyOrID)
			}
			if p == nil || b.ProjectKey != p.Key {
				continue
			}
		}
		if typ != "" && typ != "scrum" {
			continue
		}
		out = append(out, *b)
	}
	_ = s.markDirtyLocked() // materialization is a write, even when every row filtered out
	return out
}

// Board is one board by id, without materializing. An id the client never
// saw cannot exist, so no lazy creation here.
func (s *Store) Board(id int64) *model.Board {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.boardByIDLocked(id)
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// BoardSprints lists a board's sprints in creation order, optionally
// filtered to a state set ({"active","future"} — empty means all).
func (s *Store) BoardSprints(boardID int64, states []string) ([]model.Sprint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.boardByIDLocked(boardID) == nil {
		return nil, errNotFound("board", strconv.FormatInt(boardID, 10))
	}
	want := map[string]bool{}
	for _, st := range states {
		want[st] = true
	}
	out := []model.Sprint{}
	for _, sp := range s.sprintsByBoardLocked(boardID) {
		if len(want) > 0 && !want[sp.State] {
			continue
		}
		out = append(out, *sp)
	}
	return out, nil
}

// Sprint is one sprint by id.
func (s *Store) Sprint(id int64) (*model.Sprint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sp := s.sprintByIDLocked(id)
	if sp == nil {
		return nil, errNotFound("sprint", strconv.FormatInt(id, 10))
	}
	cp := *sp
	return &cp, nil
}

// SprintIssues is every issue whose current sprint (SprintIDs tail) is id,
// in key order — the renderer's order for GET /sprint/{id}/issue.
func (s *Store) SprintIssues(id int64) ([]*model.Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sprintByIDLocked(id) == nil {
		return nil, errNotFound("sprint", strconv.FormatInt(id, 10))
	}
	var out []*model.Issue
	for _, iss := range s.allIssuesLocked() {
		if n := len(iss.SprintIDs); n > 0 && iss.SprintIDs[n-1] == id {
			out = append(out, iss)
		}
	}
	return out, nil
}

// CreateSprint is POST /rest/agile/1.0/sprint: a future sprint on a board.
// Absent dates stay absent — the API omits unset dates rather than
// inventing them.
func (s *Store) CreateSprint(boardID int64, name, goal, startDate, endDate string) (*model.Sprint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boardByIDLocked(boardID) == nil {
		return nil, errNotFound("board", strconv.FormatInt(boardID, 10))
	}
	if name == "" {
		return nil, FieldError{Field: "name", Msg: "Sprint name is required"}
	}
	sp := &model.Sprint{
		ID: int64(s.nextSeqLocked("sprint")), Name: name, Goal: goal,
		State: model.SprintFuture, BoardID: boardID,
		StartDate: startDate, EndDate: endDate,
	}
	s.putSprintLocked(sp)
	if err := s.markDirtyLocked(); err != nil {
		return nil, err
	}
	cp := *sp
	return &cp, nil
}

// SprintPatch is the Agile API's partial sprint update: only the keys the
// request sent are applied. The handler decodes presence, not null-ness.
type SprintPatch struct {
	Name      *string
	Goal      *string
	State     *string
	StartDate *string
	EndDate   *string
}

// UpdateSprint is POST/PUT /rest/agile/1.0/sprint/{id}. Name, goal and
// dates are plain sets; state runs the two-transition machine:
//
//	future → active  needs a startDate and an endDate (after the patch),
//	                   stamps activatedDate from the store clock;
//	active → closed  stamps completeDate and sweeps the sprint's issues —
//	                   done ones (statusCategory.key "done", never a
//	                   localized name) keep the sprint, the rest move to
//	                   the backlog;
//	anything else    is a 400 naming the from/to states.
//
// A patch whose state equals the current one is a no-op, not a transition.
func (s *Store) UpdateSprint(id int64, patch SprintPatch, authorID string) (*model.Sprint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.sprintByIDLocked(id)
	if sp == nil {
		return nil, errNotFound("sprint", strconv.FormatInt(id, 10))
	}
	cur := *sp
	if patch.Name != nil {
		cur.Name = *patch.Name
	}
	if patch.Goal != nil {
		cur.Goal = *patch.Goal
	}
	if patch.StartDate != nil {
		cur.StartDate = *patch.StartDate
	}
	if patch.EndDate != nil {
		cur.EndDate = *patch.EndDate
	}
	if patch.State != nil && *patch.State != cur.State {
		switch {
		case cur.State == model.SprintFuture && *patch.State == model.SprintActive:
			if cur.StartDate == "" || cur.EndDate == "" {
				return nil, fmt.Errorf("startDate and endDate are required to start a sprint")
			}
			cur.State = model.SprintActive
			cur.ActivatedDate = clock.Format(s.clk.Tick())
		case cur.State == model.SprintActive && *patch.State == model.SprintClosed:
			cur.State = model.SprintClosed
			cur.CompleteDate = clock.Format(s.clk.Tick())
			s.sweepSprintIssuesLocked(&cur, authorID)
		default:
			return nil, fmt.Errorf("cannot transition sprint from %s to %s", cur.State, *patch.State)
		}
	}
	s.putSprintLocked(&cur)
	if err := s.markDirtyLocked(); err != nil {
		return nil, err
	}
	cp := cur
	return &cp, nil
}

// sweepSprintIssuesLocked is the close half: every issue whose current
// sprint is sp moves to the backlog unless its status category is done,
// in which case it keeps the sprint (that is how "done work stays in the
// report" survives). A swept issue loses its whole sprint list — a move
// to the backlog clears membership wholesale.
func (s *Store) sweepSprintIssuesLocked(sp *model.Sprint, authorID string) {
	for _, iss := range s.allIssuesLocked() {
		n := len(iss.SprintIDs)
		if n == 0 || iss.SprintIDs[n-1] != sp.ID {
			continue
		}
		if st := s.statusByIDLocked(iss.StatusID); st != nil && st.StatusCategory.Key == "done" {
			continue
		}
		s.clearSprintLocked(iss, authorID)
	}
}

// clearSprintLocked empties an issue's sprint list and records the
// changelog item. Caller has verified the list is non-empty.
func (s *Store) clearSprintLocked(iss *model.Issue, authorID string) {
	last := iss.SprintIDs[len(iss.SprintIDs)-1]
	name := s.sprintNameByIDLocked(last)
	iss.SprintIDs = nil
	iss.Updated = clock.Format(s.clk.Tick())
	s.appendHistoryLocked(iss, authorID, []model.HistoryItem{{
		Field: "Sprint", FieldID: sprintFieldID,
		From: strconv.FormatInt(last, 10), FromString: name,
		To: "", ToString: "",
	}})
	s.putIssueLocked(iss)
}

func (s *Store) sprintNameByIDLocked(id int64) string {
	if sp := s.sprintByIDLocked(id); sp != nil {
		return sp.Name
	}
	return ""
}

// MoveIssuesToSprint is POST /rest/agile/1.0/sprint/{id}/issue. The sprint
// must exist and not be closed. An issue already in it as current is a
// no-op; one carrying it earlier in its history moves it back to current;
// otherwise it is appended. Keys are processed in request order; an
// unknown key aborts with a not-found naming it — nothing is half-applied,
// because every mutation happens after the full key resolve.
func (s *Store) MoveIssuesToSprint(sprintID int64, keys []string, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.sprintByIDLocked(sprintID)
	if sp == nil {
		return errNotFound("sprint", strconv.FormatInt(sprintID, 10))
	}
	if sp.State == model.SprintClosed {
		return errConflict(fmt.Sprintf("sprint %d is closed", sprintID))
	}
	issues := make([]*model.Issue, 0, len(keys))
	for _, k := range keys {
		iss := s.issueByKeyLocked(k)
		if iss == nil {
			return errNotFound("issue", k)
		}
		issues = append(issues, iss)
	}
	for _, iss := range issues {
		n := len(iss.SprintIDs)
		if n > 0 && iss.SprintIDs[n-1] == sprintID {
			continue
		}
		var fromID int64
		if n > 0 {
			fromID = iss.SprintIDs[n-1]
		}
		next := make([]int64, 0, n+1)
		for _, id := range iss.SprintIDs {
			if id != sprintID {
				next = append(next, id)
			}
		}
		iss.SprintIDs = append(next, sprintID)
		iss.Updated = clock.Format(s.clk.Tick())
		s.appendHistoryLocked(iss, authorID, []model.HistoryItem{{
			Field: "Sprint", FieldID: sprintFieldID,
			From: sprintChangeRef(fromID), FromString: s.sprintNameByIDLocked(fromID),
			To: strconv.FormatInt(sprintID, 10), ToString: sp.Name,
		}})
		s.putIssueLocked(iss)
	}
	return s.markDirtyLocked()
}

// sprintChangeRef is a sprint id as a changelog From value: "" for
// "had none", never "0".
func sprintChangeRef(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// MoveIssuesToBacklog is POST /rest/agile/1.0/backlog/issue. Issues with
// no sprint are untouched — no changelog, no Updated tick.
func (s *Store) MoveIssuesToBacklog(keys []string, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	issues := make([]*model.Issue, 0, len(keys))
	for _, k := range keys {
		iss := s.issueByKeyLocked(k)
		if iss == nil {
			return errNotFound("issue", k)
		}
		issues = append(issues, iss)
	}
	for _, iss := range issues {
		if len(iss.SprintIDs) == 0 {
			continue
		}
		s.clearSprintLocked(iss, authorID)
	}
	return s.markDirtyLocked()
}
