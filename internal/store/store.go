// Package store is the deterministic Atlassian graph.
// Same fixture + same seed → same ids, timestamps, and ordering.
// The working copy is process-local SQLite (gob-blob tables); durable
// bytes are still a YAML fixture document via snapshot/restore.
package store

import (
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/midagedev/issuetap/internal/adf"
	"github.com/midagedev/issuetap/internal/clock"
	"github.com/midagedev/issuetap/internal/cql"
	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/jql"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"
)

// Store is safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	db        *sql.DB // process-local working copy; YAML persist stays durable
	seed      int64
	clk       *clock.Clock
	wallClock bool
	loc       locale.Code
	tz        *time.Location

	// prioNamesEnglish is the embedded-role flag: a standalone workspace
	// is a real tracker, so priority names stay English under every
	// locale — what a live Cloud site does (gadak GDK-597).
	prioNamesEnglish bool

	// Sequence counters stay in-process under mu. Stage 0's durable
	// snapshot is YAML; SQL is the working copy of the graph, not of
	// the id mint. Clock/persistState also stay here — they are not rows.
	seqIssue   int
	seqComment int
	seqHist    int
	seqAttach  int
	seqPage    int
	seqUser    int
	seqProj    int
	seqSpace   int

	persist *persistState
}

// Options seed a store.
type Options struct {
	Seed   int64
	Locale locale.Code
	// PersistPath arms write-through persistence: every mutation is
	// debounced to this file (atomic temp-file + rename) and Open reloads
	// it on startup, so a restarted process continues where it left off.
	PersistPath string
	// PersistDebounce is the quiet window before a write. Zero (with
	// PersistPath set) means the 1s default; negative means write on every
	// mutation, before the call returns.
	PersistDebounce time.Duration
	// WallClock stamps generated records with the machine's wall time
	// instead of the deterministic seed clock — for a standalone workspace
	// that is a real tracker, not a fixture-driven demo (gadak GDK-369).
	WallClock bool
	// PriorityNamesEnglish pins priority names to the English catalog under
	// every locale — what a live Cloud site does: the ko_KR site in
	// docs/LOCALES.md still returned Highest/High/… (gadak GDK-597). For a
	// standalone workspace that is a real tracker, not a name-trap harness.
	// Zero keeps the `serve --locale` deviation — localized priority names —
	// which exists to fail name-keyed clients loudly; see prioLoc.
	PriorityNamesEnglish bool
}

// DefaultPersistDebounce is the write-through quiet window.
const DefaultPersistDebounce = time.Second

// persistRetryMax caps the delay between persist retries after consecutive
// write failures. The first retry uses the debounce window (or
// DefaultPersistDebounce on the synchronous path).
const persistRetryMax = 30 * time.Second

// persistState is the write-through engine. Owned by Store.mu.
type persistState struct {
	path     string
	debounce time.Duration
	timer    *time.Timer
	dirty    bool
	err      error // last write error; cleared by the next success
	backoff  time.Duration
}

// New returns an empty store with default catalogs. When PersistPath is
// set, mutations are persisted; use Open to also reload an existing file.
func New(opt Options) *Store {
	if opt.Locale == "" {
		opt.Locale = locale.EN
	}
	clk := clock.New(opt.Seed)
	if opt.WallClock {
		clk = clock.NewWall()
	}
	s := &Store{
		db:               openWorkingDB(),
		seed:             opt.Seed,
		clk:              clk,
		wallClock:        opt.WallClock,
		loc:              opt.Locale,
		prioNamesEnglish: opt.PriorityNamesEnglish,
		tz:               time.FixedZone("KST", 9*3600),
	}
	if opt.PersistPath != "" {
		debounce := opt.PersistDebounce
		if debounce == 0 {
			debounce = DefaultPersistDebounce
		}
		if debounce < 0 {
			debounce = 0
		}
		s.persist = &persistState{path: opt.PersistPath, debounce: debounce}
	}
	s.installDefaultCatalog()
	return s
}

// Open is New plus startup reload: when the persistence file exists it is
// loaded as the initial graph (in place of any fixture the caller would
// have applied). A corrupt file is an error, never a silent empty store.
func Open(opt Options) (*Store, error) {
	st := New(opt)
	if opt.PersistPath == "" {
		return st, nil
	}
	_, statErr := os.Stat(opt.PersistPath)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return st, nil
		}
		return nil, fmt.Errorf("persist: stat %s: %w", opt.PersistPath, statErr)
	}
	doc, err := fixtures.Load(opt.PersistPath)
	if err != nil {
		return nil, fmt.Errorf("persist: load %s: %w", opt.PersistPath, err)
	}
	if err := st.Apply(doc); err != nil {
		// A durable flush of the just-loaded graph is not a user mutation.
		// Open still disarms persist bookkeeping below (load is not dirty).
		if !IsPersist(err) {
			return nil, fmt.Errorf("persist: apply %s: %w", opt.PersistPath, err)
		}
	}
	st.mu.Lock()
	if st.persist.timer != nil { // the load armed the debounce; disarm it
		st.persist.timer.Stop()
		st.persist.timer = nil
	}
	st.persist.dirty = false // the load itself is not a mutation
	st.persist.err = nil
	st.persist.backoff = 0
	st.mu.Unlock()
	return st, nil
}

// markDirtyLocked arms the debounced write. Called by every mutation.
// A zero debounce (from PersistDebounce < 0) writes before the mutation
// returns so a caller can choose durable-before-response. A flush
// failure in that synchronous mode is returned as PersistError; the
// in-memory change is kept and a backoff retry remains armed. Debounced
// mode still returns nil and retries in the background.
func (s *Store) markDirtyLocked() error {
	p := s.persist
	if p == nil {
		return nil
	}
	p.dirty = true
	if p.debounce <= 0 {
		s.flushPersistLocked()
		if p.err != nil {
			return PersistError{Err: p.err}
		}
		return nil
	}
	if p.timer != nil {
		p.timer.Reset(p.debounce)
		return nil
	}
	p.timer = time.AfterFunc(p.debounce, s.flushPersist)
	return nil
}

// flushPersist is the timer callback: one atomic write when dirty.
func (s *Store) flushPersist() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushPersistLocked()
}

func (s *Store) flushPersistLocked() {
	p := s.persist
	if p == nil {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if !p.dirty {
		return
	}
	if err := s.writePersistLocked(); err != nil {
		p.err = err // stays dirty; retried on a backoff timer
		s.armPersistRetryLocked()
		return
	}
	p.err = nil
	p.dirty = false
	p.backoff = 0
}

func (s *Store) armPersistRetryLocked() {
	p := s.persist
	if p == nil {
		return
	}
	delay := p.nextBackoff()
	if p.timer != nil {
		p.timer.Reset(delay)
		return
	}
	p.timer = time.AfterFunc(delay, s.flushPersist)
}

func (p *persistState) nextBackoff() time.Duration {
	if p.backoff <= 0 {
		if p.debounce > 0 {
			p.backoff = p.debounce
		} else {
			p.backoff = DefaultPersistDebounce
		}
		return p.backoff
	}
	next := p.backoff * 2
	if next > persistRetryMax {
		next = persistRetryMax
	}
	p.backoff = next
	return next
}

func (s *Store) writePersistLocked() error {
	b, err := fixtures.MarshalYAML(s.snapshotLocked())
	if err != nil {
		return err
	}
	return writeAtomic(s.persist.path, b)
}

// writeAtomic replaces path via same-directory temp file + rename so a
// reader (or a crash) never sees a partial document.
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Flush writes pending mutations to the persistence file now (no-op when
// persistence is not armed) and returns the write error, if any. A failed
// Flush keeps the store dirty and rearms a backoff retry so the failure
// is not silent if the caller does not retry.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.persist
	if p == nil {
		return nil
	}
	s.flushPersistLocked()
	return p.err
}

// Close flushes pending mutations and stops the debounce timer. Safe to
// call on a store without persistence. A failed flush still stops the
// timer: Close is the end of the store's lifetime.
func (s *Store) Close() error {
	err := s.Flush()
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.persist; p != nil && p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	// The working copy stays readable after Close: the previous map-backed
	// store kept the graph, and persist tests (TestRestartDoesNotReuseIds)
	// still Issue() the closed handle. Stage-3 cutover can Close the db.
	return err
}

// PersistErr is the last background (debounced) write error, for
// embedders that want to poll disk health between flushes.
func (s *Store) PersistErr() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.persist == nil {
		return nil
	}
	return s.persist.err
}

func (s *Store) installDefaultCatalog() {
	addS := func(id, name, cat string) {
		s.putStatusLocked(&model.Status{
			ID: id, Name: name,
			StatusCategory: model.StatusCategory{
				ID: model.CategoryID(cat), Key: cat,
				Name: name, ColorName: model.CategoryColor(cat),
			},
		})
	}
	addS("10000", "To Do", "new")
	addS("3", "In Progress", "indeterminate")
	addS("10003", "Done", "done")

	for _, p := range []*model.Priority{
		{ID: "1", Name: "Highest", StatusColor: "#d04437", Rank: 0},
		{ID: "2", Name: "High", StatusColor: "#f15C75", Rank: 1},
		{ID: "3", Name: "Medium", StatusColor: "#f79232", Rank: 2},
		{ID: "4", Name: "Low", StatusColor: "#707070", Rank: 3},
		{ID: "5", Name: "Lowest", StatusColor: "#999999", Rank: 4},
	} {
		s.putPriorityLocked(p)
	}

	addT := func(id, name string, hier int, sub bool) {
		s.putTypeLocked(&model.IssueType{ID: id, Name: name, HierarchyLevel: hier, Subtask: sub})
	}
	addT("10000", "Epic", 1, false)
	addT("10003", "Task", 0, false)
	addT("10007", "Bug", 0, false)
	addT("10004", "Story", 0, false)
	addT("10002", "Sub-task", -1, true)

	s.putResolutionLocked(&model.Resolution{ID: "10000", Name: "Done"})
	s.putResolutionLocked(&model.Resolution{ID: "10001", Name: "Won't Do"})
	s.putResolutionLocked(&model.Resolution{ID: "10002", Name: "Duplicate"})
	s.putResolutionLocked(&model.Resolution{ID: "10003", Name: "Cannot Reproduce"})

	s.replaceFieldsLocked(defaultFields())
}

func defaultFields() []model.FieldInfo {
	sys := []struct{ id, typ string }{
		{"issuetype", "issuetype"}, {"project", "project"}, {"status", "status"},
		{"priority", "priority"}, {"assignee", "user"}, {"reporter", "user"},
		{"summary", "string"}, {"description", "string"}, {"comment", "comments-page"},
		{"labels", "array"}, {"components", "array"}, {"fixVersions", "array"},
		{"created", "datetime"}, {"updated", "datetime"}, {"resolution", "resolution"},
		{"environment", "string"}, {"statusCategory", "statusCategory"}, {"parent", "issuelink"},
		{"duedate", "date"},
	}
	out := make([]model.FieldInfo, 0, len(sys))
	for _, f := range sys {
		out = append(out, model.FieldInfo{
			ID: f.id, Key: f.id, Name: f.id, Custom: false,
			Schema:    model.FieldSchema{Type: f.typ, System: f.id},
			Orderable: true, Navigable: true, Searchable: true,
			Clause: []string{f.id},
		})
	}
	return out
}

// Locale is the active overlay.
func (s *Store) Locale() locale.Code {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loc
}

// SetLocale changes the overlay. Data ids do not change.
func (s *Store) SetLocale(c locale.Code) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loc = c
	return s.markDirtyLocked()
}

// Seed is the determinism seed.
func (s *Store) Seed() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seed
}

// Apply upserts a fixture into the graph. Fixture rows overwrite existing
// entries of the same id/key; catalog defaults remain when the fixture
// omits them. Issue/project/user maps are not wiped first.
func (s *Store) Apply(doc fixtures.Doc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc.Seed != 0 {
		s.seed = doc.Seed
		if !s.wallClock { // a wall-clock store never falls back to seed time
			s.clk = clock.New(doc.Seed)
		}
	}
	if doc.Locale != "" {
		s.loc = locale.Parse(doc.Locale)
	}
	if doc.Timezone != "" {
		if loc, err := time.LoadLocation(doc.Timezone); err == nil {
			s.tz = loc
		} else if strings.HasPrefix(doc.Timezone, "+") || strings.HasPrefix(doc.Timezone, "-") {
			// +0900
			if t, err := time.Parse("-0700", doc.Timezone); err == nil {
				_, off := t.Zone()
				s.tz = time.FixedZone("fix", off)
			}
		}
	}

	for _, u := range doc.Users {
		s.putUser(u)
	}
	if s.userCountLocked() == 0 {
		s.putUser(fixtures.User{
			AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada Lovelace",
			Email: "you@example.com", Name: "ada", Key: "ada",
		})
	}
	for _, p := range doc.Projects {
		s.putProject(p)
	}
	for _, st := range doc.Statuses {
		s.putStatus(st)
	}
	if len(doc.Priorities) > 0 {
		s.clearPrioritiesLocked()
		for i, p := range doc.Priorities {
			s.putPriorityLocked(&model.Priority{ID: p.ID, Name: p.Name, Rank: i})
		}
	}
	for _, t := range doc.IssueTypes {
		s.putTypeLocked(&model.IssueType{
			ID: t.ID, Name: t.Name, HierarchyLevel: t.HierarchyLevel, Subtask: t.Subtask,
		})
	}
	for _, r := range doc.Resolutions {
		s.putResolutionLocked(&model.Resolution{ID: r.ID, Name: r.Name})
	}
	for _, f := range doc.Fields {
		s.upsertField(f)
	}
	for _, f := range doc.Filters {
		s.appendFilterLocked(model.Filter{
			ID: f.ID, Name: f.Name, JQL: f.JQL, Favourite: f.Favourite, Owner: f.Owner,
		})
	}
	s.clearTransitionScreensLocked()
	for _, sc := range doc.TransitionScreens {
		if sc.Status == "" {
			continue
		}
		fields := map[string]fixtures.TransitionScreenField{}
		for k, v := range sc.Fields {
			fields[k] = v
		}
		s.putTransitionScreenLocked(sc.Status, fields)
	}
	for _, p := range doc.Spaces {
		s.putSpace(p)
	}
	for _, iss := range doc.Issues {
		if err := s.putIssue(iss); err != nil {
			return err
		}
	}
	for _, p := range doc.Pages {
		s.putPage(p)
	}
	s.seedSeqsLocked()
	s.seedClockLocked()
	return s.markDirtyLocked()
}

// seedSeqsLocked continues every id sequence past the highest restored id
// so post-restart mutations cannot collide with rows that came back from
// a persistence file.
func (s *Store) seedSeqsLocked() {
	s.seedIssueSeqLocked()
	maxC, maxA, maxH := 0, 0, 0
	bumpComment := func(id string) {
		n, err := strconv.Atoi(id)
		if err != nil {
			return
		}
		// Runtime ids: issue comments 90000+seq, page comments 30000+seq
		// (both share seqComment). Authored ids outside those bands are
		// left alone — they cannot be regenerated anyway.
		if n > 90000 && n-90000 > maxC {
			maxC = n - 90000
		}
		if n > 30000 && n < 40000 && n-30000 > maxC {
			maxC = n - 30000
		}
	}
	for _, iss := range s.allIssuesLocked() {
		for _, c := range iss.Comments {
			bumpComment(c.ID)
		}
		for _, a := range iss.Attachments {
			if n, err := strconv.Atoi(a.ID); err == nil && n > 70000 && n-70000 > maxA {
				maxA = n - 70000
			}
		}
		for _, h := range iss.Histories {
			if n, err := strconv.Atoi(strings.TrimPrefix(h.ID, "h")); err == nil && n > maxH {
				maxH = n
			}
		}
	}
	for _, pid := range s.pageCommentParentIDsLocked() {
		for _, c := range s.pageCommentsLocked(pid) {
			bumpComment(c.ID)
		}
	}
	if maxC > s.seqComment {
		s.seqComment = maxC
	}
	if maxA > s.seqAttach {
		s.seqAttach = maxA
	}
	if maxH > s.seqHist {
		s.seqHist = maxH
	}
	// Runtime page ids: 20000+seqPage. Authored ids outside that band
	// (and comment ids in 30000+) are left alone.
	maxP := 0
	for _, id := range s.pageIDsLocked() {
		n, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		if n >= 20000 && n < 30000 && n-20000 > maxP {
			maxP = n - 20000
		}
	}
	if maxP > s.seqPage {
		s.seqPage = maxP
	}
}

// seedClockLocked jumps the deterministic clock past every timestamp in
// the loaded graph. Without this, mutations after a persistence reload
// would be stamped from the seed start again — earlier than rows that
// already exist — and an `updated >=` delta sync (gadak) would skip them.
// Pure function of the document, so determinism holds.
func (s *Store) seedClockLocked() {
	var max time.Time
	see := func(stamp string) {
		if stamp == "" {
			return
		}
		for _, layout := range []string{model.JiraTime, time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, stamp); err == nil {
				if t.After(max) {
					max = t
				}
				return
			}
		}
	}
	for _, iss := range s.allIssuesLocked() {
		see(iss.Created)
		see(iss.Updated)
		for _, c := range iss.Comments {
			see(c.Created)
			see(c.Updated)
		}
		for _, a := range iss.Attachments {
			see(a.Created)
		}
		for _, h := range iss.Histories {
			see(h.Created)
		}
	}
	for _, p := range s.pagesLocked() {
		see(p.When)
		for _, v := range p.Versions {
			see(v.When)
		}
		for _, c := range s.pageCommentsLocked(p.ID) {
			see(c.When)
		}
	}
	if !max.IsZero() {
		s.clk.Jump(max)
	}
}

// seedIssueSeqLocked sets seqIssue from the highest existing numeric issue
// id so CreateIssue does not reuse a fixture id (10000+seq).
func (s *Store) seedIssueSeqLocked() {
	max := 0
	for _, iss := range s.allIssuesLocked() {
		n, err := strconv.Atoi(iss.ID)
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	if max >= 10000 {
		s.seqIssue = max - 10000
	}
}

func (s *Store) putUser(u fixtures.User) *model.User {
	id := u.AccountID
	if id == "" {
		s.seqUser++
		id = fmt.Sprintf("5b10a2844c20165700ede%03d", s.seqUser)
	}
	active := true
	if u.Active != nil {
		active = *u.Active
	}
	tz := u.TimeZone
	if tz == "" {
		tz = "Asia/Seoul"
	}
	m := &model.User{
		AccountID: id, Name: u.Name, Key: first(u.Key, u.Name, id),
		DisplayName: u.DisplayName, Email: u.Email, Active: active,
		TimeZone: tz, AccountType: first(u.AccountType, "atlassian"),
		AvatarURLs: avatarURLs(id),
	}
	s.putUserLocked(m)
	return m
}

func avatarURLs(id string) map[string]string {
	// Deterministic fake avatars — no outbound. Path-only so the host is the
	// server the client already talks to.
	h := sha1.Sum([]byte(id))
	slug := hex.EncodeToString(h[:4])
	out := map[string]string{}
	for _, sz := range []string{"16x16", "24x24", "32x32", "48x48"} {
		out[sz] = "/issuetap/avatar/" + slug + "/" + sz
	}
	return out
}

func (s *Store) putProject(p fixtures.Project) {
	id := p.ID
	if id == "" {
		s.seqProj++
		id = strconv.Itoa(10000 + s.seqProj)
	}
	typ := p.TypeKey
	if typ == "" {
		typ = "software"
	}
	style := p.Style
	if style == "" {
		style = "classic"
	}
	s.putProjectLocked(&model.Project{
		ID: id, Key: p.Key, Name: p.Name, TypeKey: typ, Style: style,
		Simplified: style == "next-gen",
	})
}

// CreateProject adds a project (Cloud v3 POST /rest/api/3/project). A
// duplicate key is an error, matching Jira's 400.
func (s *Store) CreateProject(key, name string) (*model.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		return nil, fmt.Errorf("project key is required")
	}
	if s.projectByKeyLocked(key) != nil {
		return nil, fmt.Errorf("Project '%s' uses this project key.", key)
	}
	if name == "" {
		name = key
	}
	s.putProject(fixtures.Project{Key: key, Name: name})
	p := *s.projectByKeyLocked(key)
	return &p, s.markDirtyLocked()
}

func (s *Store) putStatus(st fixtures.Status) {
	cat := st.Category
	if cat == "" {
		cat = "new"
	}
	s.putStatusLocked(&model.Status{
		ID: st.ID, Name: st.Name,
		StatusCategory: model.StatusCategory{
			ID: model.CategoryID(cat), Key: cat,
			Name: st.Name, ColorName: model.CategoryColor(cat),
		},
	})
}

func (s *Store) putSpace(p fixtures.Space) {
	id := p.ID
	if id == "" {
		s.seqSpace++
		id = strconv.Itoa(40000 + s.seqSpace)
	}
	typ := p.Type
	if typ == "" {
		typ = "global"
	}
	s.putSpaceLocked(&model.Space{
		ID: id, Key: p.Key, Name: p.Name, Type: typ, Status: "current",
		HomepageID: p.Homepage,
	})
}

func (s *Store) putIssue(in fixtures.Issue) error {
	id := in.ID
	if id == "" {
		s.seqIssue++
		id = strconv.Itoa(10000 + s.seqIssue)
	}
	project := in.Project
	if project == "" {
		project, _, _ = strings.Cut(in.Key, "-")
	}
	if s.projectByKeyLocked(project) == nil {
		s.putProject(fixtures.Project{Key: project, Name: project})
	}
	created := in.Created
	if created == "" {
		created = clock.Format(s.clk.Tick())
	}
	updated := in.Updated
	if updated == "" {
		updated = created
	}
	iss := &model.Issue{
		ID: id, Key: in.Key, Summary: in.Summary,
		DescriptionText: in.Description, DescriptionADF: adf.Doc(in.Description),
		EnvironmentText: in.Environment, EnvironmentADF: adf.Doc(in.Environment),
		IssueTypeID:  s.resolveType(in.Type),
		StatusID:     s.resolveStatus(in.Status),
		PriorityID:   s.resolvePriority(in.Priority),
		AssigneeID:   s.resolveUser(in.Assignee),
		ReporterID:   s.resolveUser(first(in.Reporter, in.Creator)),
		CreatorID:    s.resolveUser(first(in.Creator, in.Reporter)),
		ProjectKey:   project,
		ParentKey:    in.Parent,
		Labels:       append([]string{}, in.Labels...),
		Duedate:      in.Duedate,
		ResolutionID: s.resolveResolution(in.Resolution),
		Created:      created,
		Updated:      updated,
		Custom:       in.Custom,
	}
	promoteDueDateFromCustom(iss)
	for _, n := range in.Components {
		iss.Components = append(iss.Components, model.Named{ID: slugID(n), Name: n})
	}
	for _, n := range in.FixVersions {
		iss.FixVersions = append(iss.FixVersions, model.Named{ID: slugID(n), Name: n})
	}
	for _, n := range in.Versions {
		iss.Versions = append(iss.Versions, model.Named{ID: slugID(n), Name: n})
	}
	for _, c := range in.Comments {
		iss.Comments = append(iss.Comments, s.makeComment(c, created))
	}
	for _, pr := range in.DevPRs {
		mpr := model.DevPR{
			ID: pr.ID, URL: pr.URL, Name: pr.Name,
			Status: first(pr.Status, "OPEN"), Updated: first(pr.Updated, created),
		}
		if pr.Author != "" {
			mpr.Author = &model.DevAuthor{Name: pr.Author}
		}
		if pr.Branch != "" {
			mpr.Source = &model.DevSource{Branch: pr.Branch}
		}
		if pr.ActorAccountID != "" || pr.ActorDisplayName != "" {
			mpr.Actor = &model.DevActor{AccountID: pr.ActorAccountID, DisplayName: pr.ActorDisplayName}
		}
		iss.DevPRs = append(iss.DevPRs, mpr)
	}
	for _, dep := range in.Deployments {
		mdep := model.DevDeployment{
			ID:  first(dep.ID, first(dep.URL, "environment:"+dep.Environment)),
			URL: dep.URL, Environment: dep.Environment, State: dep.State,
			Updated: first(dep.Updated, created),
		}
		if dep.ActorAccountID != "" || dep.ActorDisplayName != "" {
			mdep.Actor = &model.DevActor{AccountID: dep.ActorAccountID, DisplayName: dep.ActorDisplayName}
		}
		iss.DevDeployments = append(iss.DevDeployments, mdep)
	}
	for _, b := range in.Builds {
		mb := model.DevBuild{
			ID:  first(b.ID, first(b.URL, "build:"+b.Number)),
			URL: b.URL, Number: b.Number, State: b.State,
			Updated: first(b.Updated, created),
		}
		if b.ActorAccountID != "" || b.ActorDisplayName != "" {
			mb.Actor = &model.DevActor{AccountID: b.ActorAccountID, DisplayName: b.ActorDisplayName}
		}
		iss.DevBuilds = append(iss.DevBuilds, mb)
	}
	for _, a := range in.Attachments {
		att, err := s.makeAttach(a, created)
		if err != nil {
			return err
		}
		iss.Attachments = append(iss.Attachments, att)
	}
	for _, l := range in.Links {
		iss.Links = append(iss.Links, model.IssueLink{TypeName: l.Type, InwardKey: l.Inward, OutwardKey: l.Outward})
	}
	if len(in.History) == 0 {
		iss.Histories = s.synthesizeHistory(iss)
	} else {
		for _, h := range in.History {
			iss.Histories = append(iss.Histories, s.makeHistory(h, iss))
		}
	}
	s.putIssueLocked(iss)
	return nil
}

func (s *Store) makeComment(c fixtures.Comment, fallback string) model.Comment {
	id := c.ID
	if id == "" {
		s.seqComment++
		id = strconv.Itoa(90000 + s.seqComment)
	}
	created := first(c.Created, fallback)
	updated := first(c.Updated, created)
	author := s.userOrDefault(c.Author)
	cm := model.Comment{
		ID: id, Author: *author, Body: adf.Doc(c.Body), BodyText: c.Body,
		Created: created, Updated: updated,
	}
	if c.Visibility != nil && (c.Visibility.Type != "" || c.Visibility.Value != "") {
		vis := model.Visibility{Type: c.Visibility.Type, Value: c.Visibility.Value}
		cm.Visibility = &vis
	}
	if c.Internal != nil {
		jsd := !*c.Internal
		cm.JsdPublic = &jsd
	}
	return cm
}

// makeAttach restores an attachment row. Content precedence: dataBase64
// (binary-safe), then text (readable inline), then a deterministic
// placeholder so authored fixtures without content still serve bytes.
func (s *Store) makeAttach(a fixtures.Attachment, fallback string) (model.Attachment, error) {
	id := a.ID
	if id == "" {
		s.seqAttach++
		id = strconv.Itoa(70000 + s.seqAttach)
	}
	mime := a.MimeType
	if mime == "" {
		mime = "text/plain"
	}
	var body []byte
	switch {
	case a.DataBase64 != "":
		b, err := base64.StdEncoding.DecodeString(a.DataBase64)
		if err != nil {
			return model.Attachment{}, fmt.Errorf("attachment %s (%s): dataBase64: %w", id, a.Filename, err)
		}
		body = b
	case a.Text != "":
		body = []byte(a.Text)
	default:
		body = []byte("issuetap fixture attachment " + a.Filename)
	}
	s.putAttachBytesLocked(id, body)
	media := uuid5(id)
	return model.Attachment{
		ID: id, Filename: a.Filename, MimeType: mime, Size: int64(len(body)),
		Author: *s.userOrDefault(a.Author), Created: first(a.Created, fallback),
		MediaID: media,
	}, nil
}

func (s *Store) makeHistory(h fixtures.History, iss *model.Issue) model.History {
	id := h.ID
	if id == "" {
		s.seqHist++
		id = "h" + strconv.Itoa(s.seqHist)
	}
	out := model.History{ID: id, Created: h.At, Author: *s.userOrDefault(h.Author)}
	for _, it := range h.Items {
		fid := it.FieldID
		if fid == "" {
			fid = normalizeFieldID(it.Field)
		}
		fromS, toS := it.FromString, it.ToString
		if fromS == "" {
			fromS = s.displayFor(fid, it.From)
		}
		if toS == "" {
			toS = s.displayFor(fid, it.To)
		}
		out.Items = append(out.Items, model.HistoryItem{
			Field: it.Field, FieldID: fid,
			From: it.From, FromString: fromS, To: it.To, ToString: toS,
		})
	}
	return out
}

func (s *Store) synthesizeHistory(iss *model.Issue) []model.History {
	// A status that is not the first "new" status gets a created→current
	// progression so time-in-status is computable.
	firstNew := s.firstStatus("new")
	if firstNew == nil || iss.StatusID == "" || iss.StatusID == firstNew.ID {
		return nil
	}
	s.seqHist++
	id := "h" + strconv.Itoa(s.seqHist)
	at := iss.Updated
	if iss.Created != "" {
		// Midpoint-ish: use updated so clients see a change after create.
		at = iss.Updated
	}
	fromS := s.displayFor("status", firstNew.ID)
	toS := s.displayFor("status", iss.StatusID)
	return []model.History{{
		ID: id, Created: at, Author: *s.userOrDefault(iss.CreatorID),
		Items: []model.HistoryItem{{
			Field: "status", FieldID: "status",
			From: firstNew.ID, FromString: fromS,
			To: iss.StatusID, ToString: toS,
		}},
	}}
}

func (s *Store) firstStatus(cat string) *model.Status {
	var ids []string
	for _, st := range s.statusesLocked() {
		if st.StatusCategory.Key == cat {
			ids = append(ids, st.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return s.statusByIDLocked(ids[0])
}

func (s *Store) putPage(p fixtures.Page) {
	id := p.ID
	if id == "" {
		s.seqPage++
		id = strconv.Itoa(20000 + s.seqPage)
	}
	typ := p.Type
	if typ == "" {
		typ = "page"
	}
	st := p.Status
	if st == "" {
		st = "current"
	}
	ver := p.Version
	if ver <= 0 {
		ver = 1
	}
	when := formatConfluenceWhen(s.clk, p.When)
	var ancestors []string
	if p.Parent != "" {
		ancestors = []string{p.Parent}
	}
	authorID := s.resolveUser(p.Author)
	pg := &model.Page{
		ID: id, Type: typ, Status: st, Title: p.Title, SpaceKey: p.Space,
		Version: ver, When: when, AuthorID: authorID,
		BodyADF: adf.Doc(p.Body), BodyText: p.Body, BodyStorage: adf.StorageXHTML(p.Body),
		Labels: append([]string{}, p.Labels...), Ancestors: ancestors,
		WebUI: fmt.Sprintf("/spaces/%s/pages/%s", p.Space, id),
	}
	if len(p.Versions) == 0 {
		// Authored fixtures historically stored only the current stamp.
		// Synthesize one row so GET /version has something to serve.
		pg.Versions = []model.PageVersion{{
			Number: ver, When: when, AuthorID: authorID,
		}}
	} else {
		for _, v := range p.Versions {
			n := v.Number
			if n <= 0 {
				n = 1
			}
			vwhen := v.When
			if vwhen == "" {
				vwhen = when
			}
			pg.Versions = append(pg.Versions, model.PageVersion{
				Number: n, When: vwhen, AuthorID: s.resolveUser(first(v.Author, p.Author)),
				Message: v.Message, MinorEdit: v.MinorEdit,
			})
		}
		sort.Slice(pg.Versions, func(i, j int) bool { return pg.Versions[i].Number < pg.Versions[j].Number })
		last := pg.Versions[len(pg.Versions)-1]
		if pg.Version < last.Number {
			pg.Version = last.Number
		}
		pg.When = last.When
		if last.AuthorID != "" {
			pg.AuthorID = last.AuthorID
		}
	}
	s.putPageLocked(pg)
	if sp := s.spaceByKeyLocked(p.Space); sp != nil && sp.HomepageID == "" {
		sp.HomepageID = id
		s.putSpaceLocked(sp)
	}
	for _, c := range p.Comments {
		cid := c.ID
		if cid == "" {
			s.seqComment++
			cid = strconv.Itoa(30000 + s.seqComment)
		}
		cwhen := first(c.When, when)
		parent := id
		if c.ReplyTo != "" {
			parent = c.ReplyTo
		}
		s.appendPageCommentLocked(parent, model.PageComment{
			ID: cid, Title: "Re: " + p.Title, ParentID: parent,
			BodyADF: adf.Doc(c.Body), BodyText: c.Body, Version: 1, When: cwhen,
			AuthorID: s.resolveUser(c.Author),
		})
	}
}

func (s *Store) resolveUser(ref string) string {
	if ref == "" {
		return ""
	}
	if u := s.userByKeyLocked(ref); u != nil {
		return u.AccountID
	}
	if u := s.userByEmailLocked(ref); u != nil {
		return u.AccountID
	}
	for _, u := range s.usersLocked() {
		if u.DisplayName == ref || u.Name == ref || u.Key == ref {
			return u.AccountID
		}
	}
	// Implicit user so fixtures can name people without a users: block.
	u := s.putUser(fixtures.User{DisplayName: ref, AccountID: ref})
	return u.AccountID
}

func (s *Store) userOrDefault(ref string) *model.User {
	id := s.resolveUser(ref)
	if id != "" {
		if u := s.userByAccountLocked(id); u != nil {
			cp := *u
			return &cp
		}
	}
	if users := s.usersLocked(); len(users) > 0 {
		cp := *users[0]
		return &cp
	}
	u := s.putUser(fixtures.User{
		AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada Lovelace",
		Email: "you@example.com",
	})
	cp := *u
	return &cp
}

// EnsureActor returns the user an X-Issuetap-Actor slug names, creating it
// when unknown (gadak GDK-588): the slug is the accountId itself, and the
// account type is "agent" so agent-authored records render as agent
// accounts. The display name is the X-Issuetap-Actor-Name verbatim; a
// nameless slug gets a deterministic friendly alias (GDK-593,
// actorAliasLocked). Lookup is the users-map key only (accountId or DC
// username alias) — display names are never matched, and creation only
// happens when the key is free, so one agent cannot silently become
// another user and no alias is clobbered.
func (s *Store) EnsureActor(slug, name string) *model.User {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u := s.userByKeyLocked(slug); u != nil {
		cp := *u
		return &cp
	}
	if name == "" {
		name = s.actorAliasLocked(slug)
	}
	u := s.putUser(fixtures.User{
		AccountID: slug, DisplayName: name, AccountType: "agent",
	})
	// identity() has no error channel: a durable-persist failure stays on
	// PersistErr and the backoff retry, and the write that follows
	// (comment, transition, …) re-flushes the same snapshot with this user.
	_ = s.markDirtyLocked()
	cp := *u
	return &cp
}

func (s *Store) resolveType(ref string) string {
	if ref == "" {
		return "10003" // Task
	}
	if t := s.typeByIDLocked(ref); t != nil {
		return t.ID
	}
	for _, t := range s.typesLocked() {
		if strings.EqualFold(t.Name, ref) {
			return t.ID
		}
	}
	// Create so a fixture can introduce a site-specific type.
	s.putTypeLocked(&model.IssueType{ID: ref, Name: ref})
	return ref
}

func (s *Store) resolveStatus(ref string) string {
	if ref == "" {
		return "10000"
	}
	if st := s.statusByIDLocked(ref); st != nil {
		return st.ID
	}
	for _, st := range s.statusesLocked() {
		if strings.EqualFold(st.Name, ref) {
			return st.ID
		}
	}
	s.putStatusLocked(&model.Status{
		ID: ref, Name: ref,
		StatusCategory: model.StatusCategory{ID: 2, Key: "new", Name: ref, ColorName: "blue-gray"},
	})
	return ref
}

func (s *Store) resolvePriority(ref string) string {
	if ref == "" {
		return "3"
	}
	if p := s.priorityByIDLocked(ref); p != nil {
		return p.ID
	}
	for _, p := range s.prioritiesLocked() {
		if strings.EqualFold(p.Name, ref) {
			return p.ID
		}
	}
	return "3"
}

func (s *Store) resolveResolution(ref string) string {
	if ref == "" {
		return ""
	}
	if r := s.resolutionByIDLocked(ref); r != nil {
		return r.ID
	}
	for _, r := range s.resolutionsLocked() {
		if strings.EqualFold(r.Name, ref) {
			return r.ID
		}
	}
	return ref
}

func (s *Store) displayFor(fieldID, id string) string {
	switch fieldID {
	case "status":
		if st := s.statusByIDLocked(id); st != nil {
			return st.Name
		}
	case "priority":
		if p := s.priorityByIDLocked(id); p != nil {
			return p.Name
		}
	case "issuetype":
		if t := s.typeByIDLocked(id); t != nil {
			return t.Name
		}
	case "assignee", "reporter":
		if u := s.userByAccountLocked(id); u != nil {
			return u.DisplayName
		}
	}
	return id
}

func normalizeFieldID(field string) string {
	switch strings.ToLower(field) {
	case "status", "상태", "ステータス":
		return "status"
	case "assignee", "담당자", "担当者":
		return "assignee"
	case "priority", "우선 순위", "우선순위", "優先度":
		return "priority"
	case "issuetype", "issue type", "이슈 유형", "課題タイプ":
		return "issuetype"
	case "resolution", "해결":
		return "resolution"
	}
	return field
}

func slugID(name string) string {
	h := sha1.Sum([]byte(name))
	return hex.EncodeToString(h[:4])
}

// readableText reports whether attachment bytes can snapshot as inline
// `text` instead of base64: valid UTF-8 with no control characters beyond
// tab/newline/carriage return.
func readableText(b []byte) (string, bool) {
	if !utf8.Valid(b) {
		return "", false
	}
	for _, r := range string(b) {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return string(b), true
}

func uuid5(id string) string {
	h := sha1.Sum([]byte("issuetap-media:" + id))
	// Format as UUID.
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func first(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// prioLoc is the one owner of the serve-vs-embedded priority-locale split.
// A standalone workspace is a real tracker, and the live ko_KR site kept
// priority names English (docs/LOCALES.md) — so the embedded role pins
// priorities to the EN catalog under every locale. `issuetap serve
// --locale` keeps translating them; that name trap is the product of the
// serve role (gadak GDK-597). Callers must hold s.mu (or neither lock —
// the field is immutable after New).
func (s *Store) prioLoc() locale.Code {
	if s.prioNamesEnglish {
		return locale.EN
	}
	return s.loc
}

// Lookup is the JQL resolver.
func (s *Store) Lookup() jql.Lookup {
	return jql.Lookup{
		Status: func(id string) *model.Status {
			if st := s.statusByIDLocked(id); st != nil {
				cp := locale.OverlayStatus(s.loc, *st)
				return &cp
			}
			return nil
		},
		IssueType: func(id string) *model.IssueType {
			if t := s.typeByIDLocked(id); t != nil {
				cp := locale.OverlayIssueType(s.loc, *t)
				return &cp
			}
			return nil
		},
		Priority: func(id string) *model.Priority {
			if p := s.priorityByIDLocked(id); p != nil {
				cp := locale.OverlayPriority(s.prioLoc(), *p)
				return &cp
			}
			return nil
		},
		User: func(id string) *model.User {
			if u := s.userByAccountLocked(id); u != nil {
				cp := *u
				return &cp
			}
			return nil
		},
		Location: s.tz,
	}
}

// Search runs JQL and pages by nextPageToken (opaque offset string).
func (s *Store) Search(jqlText string, offset, max int) (issues []*model.Issue, total int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, err := jql.Parse(jqlText)
	if err != nil {
		return nil, 0, err
	}
	all := jql.Filter(s.allIssuesLocked(), q, s.Lookup(), 0, -1)
	total = len(all)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		return nil, total, nil
	}
	end := total
	if max > 0 && offset+max < end {
		end = offset + max
	}
	return all[offset:end], total, nil
}

// Count is approximate-count.
func (s *Store) Count(jqlText string) (int, error) {
	_, n, err := s.Search(jqlText, 0, -1)
	return n, err
}

// Issue by key or id.
func (s *Store) Issue(keyOrID string) *model.Issue {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.issueLocked(keyOrID)
}

// LinkDevPR upserts one pull-request link on an issue, keyed by URL (the
// idempotent key, same rule Linear uses for attachments). Status defaults to
// OPEN; an existing row's empty fields are kept unless the new call fills
// them — author, source, and actor included, so a re-POST from a client
// that predates GDK-589 cannot erase them (a nil side means "not sent",
// an empty string never overwrites a stored value). The actor is whatever
// the caller stamped (the API layer stamps the request identity), so each
// re-POST reattributes the link to its latest writer.
// issueLocked resolves a key or numeric id; caller holds s.mu (write, via
// the Link* dev-link methods).
func (s *Store) issueLocked(keyOrID string) *model.Issue {
	if iss := s.issueByKeyLocked(keyOrID); iss != nil {
		return iss
	}
	return s.issueByIDLocked(keyOrID)
}

func (s *Store) LinkDevPR(keyOrID string, pr model.DevPR) (model.DevPR, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueLocked(keyOrID)
	if iss == nil {
		return model.DevPR{}, errNotFound("issue", keyOrID)
	}
	if pr.Status == "" {
		pr.Status = "OPEN"
	}
	pr.Updated = clock.Format(s.clk.Tick())
	if pr.ID == "" {
		pr.ID = pr.URL
	}
	// A dev-PR link is a change to the issue: bump issue.Updated so gadak's
	// incremental sync (updated >= watermark) actually pulls it. Without this
	// the PR lands in the persist file but stays invisible until some other
	// mutation moves the watermark (GDK-537).
	iss.Updated = pr.Updated
	for i := range iss.DevPRs {
		if iss.DevPRs[i].URL == pr.URL {
			if pr.Name == "" {
				pr.Name = iss.DevPRs[i].Name
			}
			if pr.Author == nil {
				pr.Author = iss.DevPRs[i].Author
			}
			if pr.Source == nil {
				pr.Source = iss.DevPRs[i].Source
			}
			if pr.Actor == nil {
				pr.Actor = iss.DevPRs[i].Actor
			}
			iss.DevPRs[i] = pr
			s.putIssueLocked(iss)
			return pr, s.markDirtyLocked()
		}
	}
	iss.DevPRs = append(iss.DevPRs, pr)
	s.putIssueLocked(iss)
	return pr, s.markDirtyLocked()
}

// LinkDevDeployment upserts one deployment record, keyed by ID (the url,
// or environment:<env> when none was given) — the same merge rule as
// LinkDevPR: an empty field never overwrites a stored value, the actor is
// whatever the caller stamped, and each write reattributes the record to
// its latest writer (gadak GDK-592).
func (s *Store) LinkDevDeployment(keyOrID string, dep model.DevDeployment) (model.DevDeployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueLocked(keyOrID)
	if iss == nil {
		return model.DevDeployment{}, errNotFound("issue", keyOrID)
	}
	dep.Updated = clock.Format(s.clk.Tick())
	if dep.ID == "" {
		dep.ID = first(dep.URL, "environment:"+dep.Environment)
	}
	iss.Updated = dep.Updated
	for i := range iss.DevDeployments {
		if iss.DevDeployments[i].ID == dep.ID {
			if dep.URL == "" {
				dep.URL = iss.DevDeployments[i].URL
			}
			if dep.Environment == "" {
				dep.Environment = iss.DevDeployments[i].Environment
			}
			if dep.Actor == nil {
				dep.Actor = iss.DevDeployments[i].Actor
			}
			iss.DevDeployments[i] = dep
			s.putIssueLocked(iss)
			return dep, s.markDirtyLocked()
		}
	}
	iss.DevDeployments = append(iss.DevDeployments, dep)
	s.putIssueLocked(iss)
	return dep, s.markDirtyLocked()
}

// LinkDevBuild upserts one build record, keyed by ID (the url, or
// build:<number> when none was given) — same merge rule as LinkDevPR
// (gadak GDK-592).
func (s *Store) LinkDevBuild(keyOrID string, b model.DevBuild) (model.DevBuild, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueLocked(keyOrID)
	if iss == nil {
		return model.DevBuild{}, errNotFound("issue", keyOrID)
	}
	b.Updated = clock.Format(s.clk.Tick())
	if b.ID == "" {
		b.ID = first(b.URL, "build:"+b.Number)
	}
	iss.Updated = b.Updated
	for i := range iss.DevBuilds {
		if iss.DevBuilds[i].ID == b.ID {
			if b.URL == "" {
				b.URL = iss.DevBuilds[i].URL
			}
			if b.Number == "" {
				b.Number = iss.DevBuilds[i].Number
			}
			if b.Actor == nil {
				b.Actor = iss.DevBuilds[i].Actor
			}
			iss.DevBuilds[i] = b
			s.putIssueLocked(iss)
			return b, s.markDirtyLocked()
		}
	}
	iss.DevBuilds = append(iss.DevBuilds, b)
	s.putIssueLocked(iss)
	return b, s.markDirtyLocked()
}

// IssueLinkTypes is GET /rest/api/3/issueLinkType. The catalog is a fixed
// Cloud-default table owned by model.DefaultIssueLinkTypes.
func (s *Store) IssueLinkTypes() []model.IssueLinkType {
	return model.DefaultIssueLinkTypes()
}

// ErrSelfLink is POST /issueLink when outward and inward resolve to one issue.
var ErrSelfLink = errors.New("Cannot link an issue to itself.")

// AddIssueLink is POST /rest/api/3/issueLink. typeID or typeName selects a
// catalog row (id wins). Issue refs are keys or ids, same lookup as parent.
//
// Duplicate handling: the same catalog type + same outward key + same
// inward key is a successful no-op. HTTP still returns 201, but a second
// issuelinks element is not appended — a gadak link retry then cannot grow
// the mirror on re-read. A one-sided fixture row is healed by writing the
// missing side only.
func (s *Store) AddIssueLink(typeID, typeName, outwardRef, inwardRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lt, err := lookupIssueLinkType(typeID, typeName)
	if err != nil {
		return err
	}
	outwardRef = strings.TrimSpace(outwardRef)
	inwardRef = strings.TrimSpace(inwardRef)
	if outwardRef == "" || inwardRef == "" {
		return fmt.Errorf("outwardIssue and inwardIssue are required")
	}
	outward := s.issueByParentRefLocked(outwardRef)
	if outward == nil {
		return errNotFound("issue", outwardRef)
	}
	inward := s.issueByParentRefLocked(inwardRef)
	if inward == nil {
		return errNotFound("issue", inwardRef)
	}
	if outward.Key == inward.Key {
		return ErrSelfLink
	}

	added := false
	if !issueHasDirectedLink(outward, lt.Name, true, inward.Key) {
		outward.Links = append(outward.Links, model.IssueLink{TypeName: lt.Name, OutwardKey: inward.Key})
		added = true
	}
	if !issueHasDirectedLink(inward, lt.Name, false, outward.Key) {
		inward.Links = append(inward.Links, model.IssueLink{TypeName: lt.Name, InwardKey: outward.Key})
		added = true
	}
	if !added {
		return nil
	}
	now := clock.Format(s.clk.Tick())
	outward.Updated = now
	inward.Updated = now
	s.putIssueLocked(outward)
	s.putIssueLocked(inward)
	return s.markDirtyLocked()
}

func lookupIssueLinkType(id, name string) (*model.IssueLinkType, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	catalog := model.DefaultIssueLinkTypes()
	if id != "" {
		for i := range catalog {
			if catalog[i].ID == id {
				cp := catalog[i]
				return &cp, nil
			}
		}
		return nil, errUnknownLinkType(id, false)
	}
	if name == "" {
		return nil, fmt.Errorf("type is required")
	}
	for i := range catalog {
		if strings.EqualFold(catalog[i].Name, name) {
			cp := catalog[i]
			return &cp, nil
		}
	}
	return nil, errUnknownLinkType(name, true)
}

func issueHasDirectedLink(iss *model.Issue, typeName string, outward bool, otherKey string) bool {
	for _, l := range iss.Links {
		if !strings.EqualFold(l.TypeName, typeName) {
			continue
		}
		if outward && l.OutwardKey == otherKey {
			return true
		}
		if !outward && l.InwardKey == otherKey {
			return true
		}
	}
	return false
}

// Projects lists projects ordered by key.
func (s *Store) Projects() []*model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.projectsLocked()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Project by key.
func (s *Store) Project(key string) *model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projectByKeyLocked(key)
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// projectByIDOrKey matches Cloud's {projectIdOrKey} path segment.
func (s *Store) projectByIDOrKey(idOrKey string) *model.Project {
	if p := s.Project(idOrKey); p != nil {
		return p
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p := s.projectByIDLocked(idOrKey); p != nil {
		cp := *p
		return &cp
	}
	return nil
}

// Statuses lists every status (localized copies).
func (s *Store) Statuses() []model.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.statusIDsLocked()
	out := make([]model.Status, 0, len(ids))
	for _, id := range ids {
		out = append(out, locale.OverlayStatus(s.loc, *s.statusByIDLocked(id)))
	}
	return out
}

// Status by id.
func (s *Store) Status(id string) *model.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.statusByIDLocked(id)
	if st == nil {
		return nil
	}
	cp := locale.OverlayStatus(s.loc, *st)
	return &cp
}

// Priorities most-urgent first.
func (s *Store) Priorities() []model.Priority {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.prioritiesLocked()
	out := make([]model.Priority, 0, len(list))
	for _, p := range list {
		out = append(out, locale.OverlayPriority(s.prioLoc(), *p))
	}
	return out
}

// Priority by id.
func (s *Store) Priority(id string) *model.Priority {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.priorityByIDLocked(id)
	if p == nil {
		return nil
	}
	cp := locale.OverlayPriority(s.prioLoc(), *p)
	return &cp
}

// IssueTypes lists types.
func (s *Store) IssueTypes() []model.IssueType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.typesLocked()
	out := make([]model.IssueType, 0, len(list))
	for _, t := range list {
		out = append(out, locale.OverlayIssueType(s.loc, *t))
	}
	return out
}

// IssueType by id.
func (s *Store) IssueType(id string) *model.IssueType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.typeByIDLocked(id)
	if t == nil {
		return nil
	}
	cp := locale.OverlayIssueType(s.loc, *t)
	return &cp
}

// Resolutions lists resolutions.
func (s *Store) Resolutions() []model.Resolution {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.resolutionsLocked()
	out := make([]model.Resolution, 0, len(list))
	for _, r := range list {
		out = append(out, locale.OverlayResolution(s.loc, *r))
	}
	return out
}

// Fields is the field catalog with localized names.
func (s *Store) Fields() []model.FieldInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fields := s.fieldsLocked()
	out := make([]model.FieldInfo, 0, len(fields))
	for _, f := range fields {
		out = append(out, locale.OverlayField(s.loc, f))
	}
	return out
}

// Filters is GET /filter/my.
func (s *Store) Filters() []model.Filter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.filtersLocked()
}

// Users lists unique users.
func (s *Store) Users() []model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.usersLocked()
	out := make([]model.User, 0, len(list))
	for _, u := range list {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out
}

// User by accountId, username, or email.
func (s *Store) User(ref string) *model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u := s.userByKeyLocked(ref); u != nil {
		cp := *u
		return &cp
	}
	if u := s.userByEmailLocked(ref); u != nil {
		cp := *u
		return &cp
	}
	return nil
}

// UserByEmail looks up the login email.
func (s *Store) UserByEmail(email string) *model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u := s.userByEmailLocked(email); u != nil {
		cp := *u
		return &cp
	}
	return nil
}

// SearchUsers filters by query (displayName / email / name).
func (s *Store) SearchUsers(query string, max int) []model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(query)
	var out []model.User
	for _, u := range s.usersLocked() {
		if q == "" || strings.Contains(strings.ToLower(u.DisplayName), q) ||
			strings.Contains(strings.ToLower(u.Email), q) ||
			strings.Contains(strings.ToLower(u.Name), q) {
			out = append(out, *u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// DefaultUser is the first user (used for /myself when the credential email
// is not in the fixture).
func (s *Store) DefaultUser() *model.User {
	users := s.Users()
	if len(users) == 0 {
		return &model.User{
			AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada Lovelace",
			Email: "you@example.com", Active: true, TimeZone: "Asia/Seoul",
			AccountType: "atlassian",
		}
	}
	u := users[0]
	return &u
}

// Spaces lists spaces. globalOnly drops personal (~) spaces the way gadak
// does when config.spaces is empty.
func (s *Store) Spaces(globalOnly bool) []model.Space {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []model.Space
	for _, sp := range s.spacesLocked() {
		if globalOnly && sp.Type != "global" {
			continue
		}
		out = append(out, *sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Space by key.
func (s *Store) Space(key string) *model.Space {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sp := s.spaceByKeyLocked(key)
	if sp == nil {
		return nil
	}
	cp := *sp
	return &cp
}

// Page by id.
func (s *Store) Page(id string) *model.Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.pageByIDLocked(id)
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// Pages lists pages.
func (s *Store) Pages() []model.Page {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.pagesLocked()
	out := make([]model.Page, 0, len(list))
	for _, p := range list {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ChildComments lists comments on a content id.
func (s *Store) ChildComments(contentID string) []model.PageComment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pageCommentsLocked(contentID)
}

// SearchPages evaluates CQL.
func (s *Store) SearchPages(cqlText string) ([]model.Page, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, err := cql.Parse(cqlText)
	if err != nil {
		return nil, err
	}
	var out []model.Page
	if q.Type == "comment" {
		for _, pid := range s.pageCommentParentIDsLocked() {
			pg := s.pageByIDLocked(pid)
			space := ""
			if pg != nil {
				space = pg.SpaceKey
			}
			for _, cm := range s.pageCommentsLocked(pid) {
				if !cql.MatchComment(q, space, cm.When) {
					continue
				}
				webui := ""
				if pg != nil {
					webui = fmt.Sprintf("/spaces/%s/pages/%s?pageId=%s", pg.SpaceKey, pg.ID, pg.ID)
				}
				out = append(out, model.Page{
					ID: cm.ID, Type: "comment", Status: "current", Title: cm.Title,
					SpaceKey: space, Version: cm.Version, When: cm.When,
					AuthorID: cm.AuthorID, BodyADF: cm.BodyADF, BodyText: cm.BodyText,
					WebUI: webui, Container: pid,
					Ancestors: func() []string {
						if pg != nil {
							return []string{pg.ID}
						}
						return nil
					}(),
				})
			}
		}
	} else {
		for _, p := range s.pagesLocked() {
			if cql.MatchPage(q, p) {
				out = append(out, *p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].When == out[j].When {
			return out[i].ID < out[j].ID
		}
		if q.OrderAsc {
			return out[i].When < out[j].When
		}
		return out[i].When > out[j].When
	})
	return out, nil
}

// PageWrite is a Cloud v1 content create/update.
type PageWrite struct {
	Title     string
	SpaceKey  string
	ParentID  string
	BodyADF   json.RawMessage
	AuthorID  string
	Message   string
	MinorEdit bool
	// Next is the requested version.number on update. Ignored on create.
	Next int
}

// CreatePage inserts a page at version 1 and appends the first history row.
func (s *Store) CreatePage(in PageWrite) (*model.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if in.SpaceKey == "" || s.spaceByKeyLocked(in.SpaceKey) == nil {
		return nil, fmt.Errorf("No space found with key: %s", in.SpaceKey)
	}
	var ancestors []string
	if in.ParentID != "" {
		parent := s.pageByIDLocked(in.ParentID)
		if parent == nil {
			return nil, fmt.Errorf("No content found with id: %s", in.ParentID)
		}
		ancestors = []string{in.ParentID}
	}
	body, text, err := parseADF(in.BodyADF)
	if err != nil {
		return nil, err
	}
	s.seqPage++
	id := strconv.Itoa(20000 + s.seqPage)
	when := formatConfluenceWhen(s.clk, "")
	author := in.AuthorID
	if author == "" {
		author = s.userOrDefault("").AccountID
	}
	ver := model.PageVersion{
		Number: 1, When: when, AuthorID: author,
		Message: in.Message, MinorEdit: in.MinorEdit,
	}
	pg := &model.Page{
		ID: id, Type: "page", Status: "current", Title: title, SpaceKey: in.SpaceKey,
		Version: 1, When: when, AuthorID: author,
		BodyADF: body, BodyText: text, BodyStorage: adf.StorageXHTML(text),
		Ancestors: ancestors, Versions: []model.PageVersion{ver},
		WebUI: fmt.Sprintf("/spaces/%s/pages/%s", in.SpaceKey, id),
	}
	s.putPageLocked(pg)
	err = s.markDirtyLocked()
	cp := *pg
	return &cp, err
}

// UpdatePage applies a Confluence optimistic-concurrency update: Next must
// equal the current version + 1. A miss is IsConflict (HTTP 409).
func (s *Store) UpdatePage(id string, in PageWrite) (*model.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pg := s.pageByIDLocked(id)
	if pg == nil {
		return nil, errNotFound("page", id)
	}
	want := pg.Version + 1
	if want <= 0 {
		want = 2
	}
	if in.Next != want {
		return nil, errConflict(fmt.Sprintf("Version must be incremented on update. Current version is: %d", pg.Version))
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if in.SpaceKey != "" && s.spaceByKeyLocked(in.SpaceKey) == nil {
		return nil, fmt.Errorf("No space found with key: %s", in.SpaceKey)
	}
	if in.ParentID != "" {
		if in.ParentID != pg.ID && s.pageByIDLocked(in.ParentID) == nil {
			return nil, fmt.Errorf("No content found with id: %s", in.ParentID)
		}
		pg.Ancestors = []string{in.ParentID}
	}
	if len(in.BodyADF) > 0 {
		body, text, err := parseADF(in.BodyADF)
		if err != nil {
			return nil, err
		}
		pg.BodyADF = body
		pg.BodyText = text
		pg.BodyStorage = adf.StorageXHTML(text)
	}
	when := formatConfluenceWhen(s.clk, "")
	author := in.AuthorID
	if author == "" {
		author = s.userOrDefault("").AccountID
	}
	pg.Title = title
	if in.SpaceKey != "" {
		pg.SpaceKey = in.SpaceKey
		pg.WebUI = fmt.Sprintf("/spaces/%s/pages/%s", in.SpaceKey, pg.ID)
	}
	pg.Version = want
	pg.When = when
	pg.AuthorID = author
	pg.Versions = append(pg.Versions, model.PageVersion{
		Number: want, When: when, AuthorID: author,
		Message: in.Message, MinorEdit: in.MinorEdit,
	})
	s.putPageLocked(pg)
	err := s.markDirtyLocked()
	cp := *pg
	return &cp, err
}

// PageVersions returns history newest-first (number descending). That is
// the Cloud v2 default and the documented order for this endpoint.
func (s *Store) PageVersions(id string) ([]model.PageVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pg := s.pageByIDLocked(id)
	if pg == nil {
		return nil, errNotFound("page", id)
	}
	out := append([]model.PageVersion(nil), pg.Versions...)
	if len(out) == 0 {
		out = []model.PageVersion{{
			Number: pg.Version, When: pg.When, AuthorID: pg.AuthorID,
		}}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

func formatConfluenceWhen(clk *clock.Clock, explicit string) string {
	if explicit != "" {
		return explicit
	}
	when := clock.Format(clk.Tick())
	if t, err := time.Parse(model.JiraTime, when); err == nil {
		return t.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	return when
}

// parseADF accepts body.atlas_doc_format.value as a JSON string of an ADF
// document or as the document object itself.
func parseADF(raw json.RawMessage) (json.RawMessage, string, error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, "", fmt.Errorf("malformed ADF")
	}
	var obj json.RawMessage
	switch s[0] {
	case '"':
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil, "", fmt.Errorf("malformed ADF")
		}
		inner = strings.TrimSpace(inner)
		if inner == "" || inner[0] != '{' || !json.Valid([]byte(inner)) {
			return nil, "", fmt.Errorf("malformed ADF")
		}
		obj = json.RawMessage(inner)
	case '{':
		if !json.Valid(raw) {
			return nil, "", fmt.Errorf("malformed ADF")
		}
		obj = json.RawMessage(s)
	default:
		return nil, "", fmt.Errorf("malformed ADF")
	}
	var node map[string]any
	if err := json.Unmarshal(obj, &node); err != nil {
		return nil, "", fmt.Errorf("malformed ADF")
	}
	if typ, _ := node["type"].(string); typ != "doc" {
		return nil, "", fmt.Errorf("malformed ADF")
	}
	return obj, adf.Plain(obj), nil
}

// AttachmentBytes returns stored bytes.
func (s *Store) AttachmentBytes(id string) ([]byte, *model.Attachment) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := s.attachBytesLocked(id)
	for _, iss := range s.allIssuesLocked() {
		for i := range iss.Attachments {
			if iss.Attachments[i].ID == id {
				a := iss.Attachments[i]
				return b, &a
			}
		}
	}
	return b, nil
}

// AttachmentByMedia resolves the media UUID that /attachment/content
// redirects to, so the download target can serve the stored bytes.
func (s *Store) AttachmentByMedia(media string) ([]byte, *model.Attachment) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, iss := range s.allIssuesLocked() {
		for i := range iss.Attachments {
			if iss.Attachments[i].MediaID == media {
				a := iss.Attachments[i]
				b, _ := s.attachBytesLocked(a.ID)
				return b, &a
			}
		}
	}
	return nil, nil
}

// Transitions for an issue: every other status in the catalog. This is a
// model, not a workflow engine. Numbering is the same function
// ApplyTransition uses so GET id and POST id cannot drift.
func (s *Store) Transitions(key string) []model.Transition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	iss := s.issueLocked(key)
	if iss == nil {
		return nil
	}
	ids := s.destinationIDsLocked(iss.StatusID)
	out := make([]model.Transition, 0, len(ids))
	for i, id := range ids {
		st := locale.OverlayStatus(s.loc, *s.statusByIDLocked(id))
		out = append(out, model.Transition{
			ID: strconv.Itoa(i + 1), Name: st.Name, ToID: id,
		})
	}
	return out
}

func (s *Store) destinationIDsLocked(fromStatus string) []string {
	var ids []string
	for _, id := range s.statusIDsLocked() {
		if id != fromStatus {
			ids = append(ids, id)
		}
	}
	return ids
}

// TransitionScreenFields is the Cloud `fields` object for the transition
// into toStatusID. Always a non-nil map: empty screen is {}.
func (s *Store) TransitionScreenFields(toStatusID string) map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transitionScreenFieldsLocked(toStatusID)
}

func (s *Store) transitionScreenFieldsLocked(toStatusID string) map[string]any {
	out := map[string]any{}
	screen, ok := s.transitionScreenLocked(toStatusID)
	if !ok {
		return out
	}
	ids := make([]string, 0, len(screen))
	for id := range screen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out[id] = s.transitionFieldMetaLocked(id, screen[id])
	}
	return out
}

func (s *Store) transitionFieldMetaLocked(id string, spec fixtures.TransitionScreenField) map[string]any {
	name := locale.FieldName(s.loc, id, id)
	schemaType := "string"
	if id == "resolution" {
		schemaType = "resolution"
	} else if f := s.fieldByIDLocked(id); f != nil && f.Schema.Type != "" {
		schemaType = f.Schema.Type
	}
	meta := map[string]any{
		"required": spec.Required,
		"name":     name,
		"schema":   map[string]any{"type": schemaType},
	}
	if id == "resolution" {
		meta["allowedValues"] = s.resolutionAllowedValuesLocked()
	}
	return meta
}

func (s *Store) resolutionAllowedValuesLocked() []any {
	ids := s.resolutionIDsLocked()
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		r := locale.OverlayResolution(s.loc, *s.resolutionByIDLocked(id))
		out = append(out, map[string]any{"id": r.ID, "name": r.Name})
	}
	return out
}

// Transition applies a synthetic transition id with no fields or update.
func (s *Store) Transition(key, transitionID string) error {
	return s.ApplyTransition(key, transitionID, "", nil, nil)
}

// ApplyTransition is POST /issue/{key}/transitions. HTTP only copies
// shape; this method owns screen checks, resolution catalog lookup, and
// persistence. Resolution is keyed by catalog id, never a localized name.
func (s *Store) ApplyTransition(key, transitionID, authorID string, fields, update map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return errNotFound("issue", key)
	}
	if err := s.applyTransitionLocked(iss, transitionID, authorID, fields, update); err != nil {
		return err
	}
	s.putIssueLocked(iss)
	return s.markDirtyLocked()
}

// applyTransitionLocked is ApplyTransition after the issue lookup: screen
// checks, resolution catalog lookup, the status change, its changelog row,
// and update.comment writes. Every validation happens before the first
// mutation, so an error leaves the issue untouched. Claim runs it under
// one lock with the assignee change (gadak GDK-591).
func (s *Store) applyTransitionLocked(iss *model.Issue, transitionID, authorID string, fields, update map[string]any) error {
	ids := s.destinationIDsLocked(iss.StatusID)
	idx, err := strconv.Atoi(transitionID)
	if err != nil || idx < 1 || idx > len(ids) {
		return errNotFound("transition", transitionID)
	}
	to := ids[idx-1]
	from := iss.StatusID
	screen, _ := s.transitionScreenLocked(to)

	if err := rejectFieldsNotOnScreen(fields, screen); err != nil {
		return err
	}
	if err := requireScreenFields(fields, screen); err != nil {
		return err
	}
	comments, err := parseTransitionComments(update)
	if err != nil {
		return err
	}

	var setResolution *string
	if raw, ok := fields["resolution"]; ok {
		id := pickID(raw)
		if id == "" || s.resolutionByIDLocked(id) == nil {
			return FieldError{Field: "resolution", Msg: "Specified resolution is not valid."}
		}
		setResolution = &id
	}

	iss.StatusID = to
	iss.Updated = clock.Format(s.clk.Tick())
	if setResolution != nil {
		iss.ResolutionID = *setResolution
	}
	if stTo := s.statusByIDLocked(to); stTo != nil && stTo.StatusCategory.Key != "done" {
		iss.ResolutionID = ""
	}
	// Cloud workflows fill a default resolution via a post-function when
	// entering done if the request (and screen) did not supply one.
	// Existing fixtures, persist files, and gadak conformance depend on
	// this remaining 10000. Required screens never reach here empty.
	if stTo := s.statusByIDLocked(to); stTo != nil && stTo.StatusCategory.Key == "done" && iss.ResolutionID == "" {
		iss.ResolutionID = "10000"
	}
	s.seqHist++
	iss.Histories = append(iss.Histories, model.History{
		ID: "h" + strconv.Itoa(s.seqHist), Created: iss.Updated,
		Author: *s.userOrDefault(authorID),
		Items: []model.HistoryItem{{
			Field: "status", FieldID: "status",
			From: from, FromString: s.displayFor("status", from),
			To: to, ToString: s.displayFor("status", to),
		}},
	})
	for _, body := range comments {
		s.addCommentLocked(iss, authorID, body, nil, nil)
	}
	return nil
}

func rejectFieldsNotOnScreen(fields map[string]any, screen map[string]fixtures.TransitionScreenField) error {
	if len(fields) == 0 {
		return nil
	}
	var extra []string
	for field := range fields {
		if _, ok := screen[field]; !ok {
			extra = append(extra, field)
		}
	}
	if len(extra) == 0 {
		return nil
	}
	sort.Strings(extra)
	f := extra[0]
	return FieldError{
		Field: f,
		Msg:   "Field '" + f + "' cannot be set. It is not on the appropriate screen, or unknown.",
	}
}

func requireScreenFields(fields map[string]any, screen map[string]fixtures.TransitionScreenField) error {
	var missing []string
	for field, spec := range screen {
		if !spec.Required {
			continue
		}
		if _, ok := fields[field]; !ok {
			missing = append(missing, field)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	f := missing[0]
	msg := f + " is required."
	if f == "resolution" {
		msg = "Resolution is required."
	}
	return FieldError{Field: f, Msg: msg}
}

func parseTransitionComments(update map[string]any) ([][]byte, error) {
	if len(update) == 0 {
		return nil, nil
	}
	var out [][]byte
	keys := make([]string, 0, len(update))
	for field := range update {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		if field != "comment" {
			return nil, fmt.Errorf("unsupported update field: %s", field)
		}
		ops, ok := update[field].([]any)
		if !ok {
			return nil, fmt.Errorf("update.comment must be an array of operations")
		}
		for _, opRaw := range ops {
			op, ok := opRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("comment update operation must be an object")
			}
			add, ok := op["add"]
			if !ok {
				return nil, fmt.Errorf("unsupported comment operation")
			}
			addObj, ok := add.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("comment add must be an object")
			}
			body, ok := addObj["body"]
			if !ok {
				return nil, fmt.Errorf("comment add needs a body")
			}
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// CommentProperty is one Cloud comment property on POST /issue/{key}/comment.
// Only key sd.public.comment is interpreted (value.internal → JsdPublic).
type CommentProperty struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// CommentWrite is POST /issue/{key}/comment after HTTP has copied the
// request shape. This method owns visibility type/value checks and
// sd.public.comment parsing. Role/group existence is not checked.
type CommentWrite struct {
	Body       []byte
	Visibility *model.Visibility
	Properties []CommentProperty
}

const sdPublicCommentProperty = "sd.public.comment"

// AddComment posts a comment with no visibility or JSM internal marker.
// body is ADF or a string.
func (s *Store) AddComment(key, authorID string, body []byte) (model.Comment, error) {
	return s.WriteComment(key, authorID, CommentWrite{Body: body})
}

// WriteComment is POST /issue/{key}/comment. HTTP only copies shape.
func (s *Store) WriteComment(key, authorID string, in CommentWrite) (model.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return model.Comment{}, errNotFound("issue", key)
	}
	vis, err := normalizeCommentVisibility(in.Visibility)
	if err != nil {
		return model.Comment{}, err
	}
	jsd := jsdPublicFromProperties(in.Properties)
	cm := s.addCommentLocked(iss, authorID, in.Body, vis, jsd)
	s.putIssueLocked(iss)
	return cm, s.markDirtyLocked()
}

func normalizeCommentVisibility(v *model.Visibility) (*model.Visibility, error) {
	if v == nil {
		return nil, nil
	}
	typeName := strings.TrimSpace(v.Type)
	value := strings.TrimSpace(v.Value)
	if typeName != "role" && typeName != "group" {
		return nil, FieldError{Field: "visibility", Msg: "type must be role or group"}
	}
	if value == "" {
		return nil, FieldError{Field: "visibility", Msg: "value is required"}
	}
	return &model.Visibility{Type: typeName, Value: value}, nil
}

func jsdPublicFromProperties(props []CommentProperty) *bool {
	for _, p := range props {
		if p.Key != sdPublicCommentProperty {
			continue
		}
		var val struct {
			Internal *bool `json:"internal"`
		}
		if err := json.Unmarshal(p.Value, &val); err != nil {
			return nil
		}
		if val.Internal == nil {
			return nil
		}
		jsd := !*val.Internal
		return &jsd
	}
	return nil
}

func (s *Store) addCommentLocked(iss *model.Issue, authorID string, body []byte, vis *model.Visibility, jsd *bool) model.Comment {
	text := adf.Plain(body)
	if text == "" && len(body) > 0 && body[0] != '{' {
		text = string(body)
		body = adf.Doc(text)
	}
	if len(body) == 0 {
		body = adf.Doc(text)
	}
	s.seqComment++
	now := clock.Format(s.clk.Tick())
	cm := model.Comment{
		ID:         strconv.Itoa(90000 + s.seqComment),
		Author:     *s.userOrDefault(authorID),
		Body:       body,
		BodyText:   text,
		Created:    now,
		Updated:    now,
		Visibility: vis,
		JsdPublic:  jsd,
	}
	iss.Comments = append(iss.Comments, cm)
	iss.Updated = now
	return cm
}

// AddPageComment posts a top-level comment on a page (Cloud v1
// POST /rest/api/content with type=comment and a page container).
func (s *Store) AddPageComment(pageID, authorID string, body json.RawMessage) (model.PageComment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pg := s.pageByIDLocked(pageID)
	if pg == nil {
		return model.PageComment{}, errNotFound("page", pageID)
	}
	adfBody, text, err := parseADF(body)
	if err != nil {
		return model.PageComment{}, err
	}
	s.seqComment++
	cm := model.PageComment{
		ID: strconv.Itoa(30000 + s.seqComment), Title: "Re: " + pg.Title,
		ParentID: pageID, BodyADF: adfBody, BodyText: text, Version: 1,
		When:     formatConfluenceWhen(s.clk, ""),
		AuthorID: s.userOrDefault(authorID).AccountID,
	}
	s.appendPageCommentLocked(pageID, cm)
	return cm, s.markDirtyLocked()
}

// SetAssignee assigns or unassigns. authorID is the acting user recorded
// as the changelog author (gadak GDK-588); accountID is the new assignee.
func (s *Store) SetAssignee(key, accountID, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return errNotFound("issue", key)
	}
	s.setAssigneeLocked(iss, accountID, authorID)
	s.putIssueLocked(iss)
	return s.markDirtyLocked()
}

// setAssigneeLocked is SetAssignee after the issue lookup: the assignee
// change and its changelog row. Claim runs it under one lock with the
// in-progress transition (gadak GDK-591).
func (s *Store) setAssigneeLocked(iss *model.Issue, accountID, authorID string) {
	from := iss.AssigneeID
	iss.AssigneeID = accountID
	iss.Updated = clock.Format(s.clk.Tick())
	s.seqHist++
	iss.Histories = append(iss.Histories, model.History{
		ID: "h" + strconv.Itoa(s.seqHist), Created: iss.Updated,
		Author: *s.userOrDefault(authorID),
		Items: []model.HistoryItem{{
			Field: "assignee", FieldID: "assignee",
			From: from, FromString: s.displayFor("assignee", from),
			To: accountID, ToString: s.displayFor("assignee", accountID),
		}},
	})
}

// ClaimResult is the outcome of a successful Claim.
type ClaimResult struct {
	Key       string
	Assignee  model.User
	Status    model.Status
	ClaimedAt string
}

// Claim is POST /issue/{key}/claim (issuetap extension, gadak GDK-591 —
// Cloud has no such route; there the client falls back to two calls).
// Assignee + in-progress transition happen as one mutation under s.mu, so
// of two agents claiming concurrently exactly one wins. Rules, in order:
//
//   - the issue is already in an in-progress status (statusCategory.key
//     "indeterminate" — never a localized name, docs/LOCALES.md) and the
//     assignee is another accountId → errConflict ("<KEY> is already
//     claimed by <displayName>") unless takeOver;
//   - the same actor already holds it → idempotent success: no
//     re-transition, no duplicate changelog row;
//   - otherwise → assignee=actor and, when the issue is not already in
//     progress, the in-progress transition. transitionID selects it
//     explicitly; empty picks the first destination whose category is
//     indeterminate, or errors "no in-progress transition available".
//
// A claim never moves an issue that is already in progress: takeover is an
// assignee change, and the auto-picked transition cannot exist anyway (a
// status is not its own destination). The transition runs before the
// assignee write so its validation errors leave the issue untouched.
// ClaimedAt is read from the changelog (when the issue entered its current
// status) — no new stored field; time-in-status stays changelog-derived.
func (s *Store) Claim(key, actorID, transitionID string, takeOver bool) (ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return ClaimResult{}, errNotFound("issue", key)
	}
	inProgress := s.issueInProgressLocked(iss)
	if inProgress && iss.AssigneeID != "" && iss.AssigneeID != actorID && !takeOver {
		return ClaimResult{}, errConflict(fmt.Sprintf(
			"%s is already claimed by %s", iss.Key, s.displayFor("assignee", iss.AssigneeID)))
	}
	if !(inProgress && iss.AssigneeID == actorID) { // not the idempotent case
		if !inProgress {
			id := transitionID
			if id == "" {
				id = s.firstInProgressTransitionLocked(iss)
			}
			if id == "" {
				return ClaimResult{}, fmt.Errorf("no in-progress transition available")
			}
			if err := s.applyTransitionLocked(iss, id, actorID, nil, nil); err != nil {
				return ClaimResult{}, err
			}
		}
		if iss.AssigneeID != actorID {
			s.setAssigneeLocked(iss, actorID, actorID)
		}
		s.putIssueLocked(iss)
		if err := s.markDirtyLocked(); err != nil {
			return ClaimResult{}, err
		}
	}
	res := ClaimResult{Key: iss.Key, ClaimedAt: s.lastStatusChangeLocked(iss)}
	if res.ClaimedAt == "" {
		res.ClaimedAt = iss.Updated
	}
	if u := s.userByAccountLocked(iss.AssigneeID); u != nil {
		res.Assignee = *u
	} else {
		res.Assignee = model.User{
			AccountID: iss.AssigneeID, DisplayName: s.displayFor("assignee", iss.AssigneeID),
		}
	}
	if st := s.statusByIDLocked(iss.StatusID); st != nil {
		res.Status = *st
	} else {
		res.Status = model.Status{ID: iss.StatusID, Name: iss.StatusID}
	}
	return res, nil
}

// issueInProgressLocked reports whether the issue's status category is
// indeterminate (Jira's in-progress category; the key is locale-stable).
func (s *Store) issueInProgressLocked(iss *model.Issue) bool {
	st := s.statusByIDLocked(iss.StatusID)
	return st != nil && st.StatusCategory.Key == "indeterminate"
}

// firstInProgressTransitionLocked is the synthetic transition id of the
// first destination whose statusCategory.key is indeterminate, in the same
// destinationIDsLocked order GET /transitions lists — or "" when none is.
func (s *Store) firstInProgressTransitionLocked(iss *model.Issue) string {
	ids := s.destinationIDsLocked(iss.StatusID)
	for i, id := range ids {
		if st := s.statusByIDLocked(id); st != nil && st.StatusCategory.Key == "indeterminate" {
			return strconv.Itoa(i + 1)
		}
	}
	return ""
}

// lastStatusChangeLocked is when the issue entered its current status:
// the newest changelog row carrying a status item with To == now. Empty
// when no such row exists (an authored fixture without history).
func (s *Store) lastStatusChangeLocked(iss *model.Issue) string {
	for i := len(iss.Histories) - 1; i >= 0; i-- {
		for _, it := range iss.Histories[i].Items {
			if it.FieldID == "status" && it.To == iss.StatusID {
				return iss.Histories[i].Created
			}
		}
	}
	return ""
}

// UpdateFields applies a fields map (summary, description, labels, …).
func (s *Store) UpdateFields(key string, fields map[string]any) error {
	return s.UpdateIssue(key, fields, nil)
}

// UpdateIssue applies Jira Cloud PUT /issue {fields, update}.
// update is processed first, then fields (Cloud order). Unsupported
// update fields return an error instead of a silent no-op.
// fixVersions and components are first-class typed arrays (not Custom);
// fields replaces the whole list, update accepts add/remove/set.
func (s *Store) UpdateIssue(key string, fields, update map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return errNotFound("issue", key)
	}
	if err := s.applyUpdateOps(iss, update); err != nil {
		return err
	}
	var parentKey *string
	if raw, ok := fields["parent"]; ok {
		childTypeID := iss.IssueTypeID
		if id := pickID(fields["issuetype"]); id != "" {
			childTypeID = id
		}
		k, err := s.resolveParentLocked(childTypeID, pickKey(raw))
		if err != nil {
			return parentFieldError(err, parentEditKeys)
		}
		parentKey = &k
	}
	for k, v := range fields {
		switch k {
		case "summary":
			if str, ok := v.(string); ok {
				iss.Summary = str
			}
		case "description":
			s.setDesc(iss, v)
		case "labels":
			iss.Labels = stringSlice(v)
		case "priority":
			iss.PriorityID = pickID(v)
		case "issuetype":
			iss.IssueTypeID = pickID(v)
		case "parent":
			if parentKey != nil {
				iss.ParentKey = *parentKey
			}
		case "duedate":
			if err := setDueDate(iss, v); err != nil {
				return err
			}
		case "assignee":
			iss.AssigneeID = pickID(v)
		case "fixVersions":
			named, err := s.resolveNamedListLocked(iss.ProjectKey, "fixVersions", v)
			if err != nil {
				return err
			}
			iss.FixVersions = named
			delete(iss.Custom, "fixVersions")
		case "components":
			named, err := s.resolveNamedListLocked(iss.ProjectKey, "components", v)
			if err != nil {
				return err
			}
			iss.Components = named
			delete(iss.Custom, "components")
		default:
			if err := s.validateCustomWriteLocked(k, v); err != nil {
				return err
			}
			if iss.Custom == nil {
				iss.Custom = map[string]any{}
			}
			iss.Custom[k] = v
		}
	}
	iss.Updated = clock.Format(s.clk.Tick())
	s.putIssueLocked(iss)
	return s.markDirtyLocked()
}

func (s *Store) applyUpdateOps(iss *model.Issue, update map[string]any) error {
	if len(update) == 0 {
		return nil
	}
	for field, raw := range update {
		ops, ok := raw.([]any)
		if !ok {
			return fmt.Errorf("update.%s must be an array of operations", field)
		}
		switch field {
		case "labels":
			if err := applyLabelOps(iss, ops); err != nil {
				return err
			}
		case "fixVersions":
			next, err := s.applyNamedOpsLocked(iss.ProjectKey, "fixVersions", iss.FixVersions, ops)
			if err != nil {
				return err
			}
			iss.FixVersions = next
			delete(iss.Custom, "fixVersions")
		case "components":
			next, err := s.applyNamedOpsLocked(iss.ProjectKey, "components", iss.Components, ops)
			if err != nil {
				return err
			}
			iss.Components = next
			delete(iss.Custom, "components")
		default:
			return fmt.Errorf("unsupported update field: %s", field)
		}
	}
	return nil
}

func applyLabelOps(iss *model.Issue, ops []any) error {
	for _, raw := range ops {
		op, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("labels update operation must be an object")
		}
		if v, ok := op["add"]; ok {
			s, err := labelOpString(v)
			if err != nil {
				return err
			}
			if !containsLabel(iss.Labels, s) {
				iss.Labels = append(iss.Labels, s)
			}
			continue
		}
		if v, ok := op["remove"]; ok {
			s, err := labelOpString(v)
			if err != nil {
				return err
			}
			iss.Labels = removeLabel(iss.Labels, s)
			continue
		}
		if v, ok := op["set"]; ok {
			iss.Labels = stringSlice(v)
			continue
		}
		return fmt.Errorf("unsupported labels operation")
	}
	return nil
}

func labelOpString(v any) (string, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", fmt.Errorf("label operation needs a non-empty string")
	}
	return s, nil
}

func containsLabel(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

func removeLabel(in []string, want string) []string {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	for _, s := range in {
		if s != want {
			out = append(out, s)
		}
	}
	return out
}

// Version and component identity is the union of {id,name} already on
// issues in the project. fixtures.Project has no versions/components list,
// so GET /project/{key}/versions and /components serve this derived
// catalog. A project with no such rows yet accepts a name as-is so a
// minimal fixture still round-trips; a present catalog rejects unknown
// id/name — Cloud does not create versions on issue edit.
func (s *Store) applyNamedOpsLocked(project, field string, current []model.Named, ops []any) ([]model.Named, error) {
	cur := append([]model.Named{}, current...)
	for _, raw := range ops {
		op, ok := raw.(map[string]any)
		if !ok {
			return nil, FieldError{Field: field, Msg: "update operation must be an object"}
		}
		if v, ok := op["add"]; ok {
			n, err := s.resolveNamedLocked(project, field, v, cur)
			if err != nil {
				return nil, err
			}
			if !namedHasID(cur, n.ID) {
				cur = append(cur, n)
			}
			continue
		}
		if v, ok := op["remove"]; ok {
			n, err := s.resolveNamedLocked(project, field, v, cur)
			if err != nil {
				return nil, err
			}
			cur = namedRemoveID(cur, n.ID)
			continue
		}
		if v, ok := op["set"]; ok {
			next, err := s.resolveNamedListLocked(project, field, v)
			if err != nil {
				return nil, err
			}
			cur = next
			continue
		}
		return nil, FieldError{Field: field, Msg: "unsupported operation"}
	}
	return cur, nil
}

func (s *Store) resolveNamedListLocked(project, field string, v any) ([]model.Named, error) {
	if v == nil {
		return []model.Named{}, nil
	}
	arr, ok := asAnySlice(v)
	if !ok {
		return nil, FieldError{Field: field, Msg: "must be an array"}
	}
	out := make([]model.Named, 0, len(arr))
	seen := map[string]bool{}
	for _, item := range arr {
		n, err := s.resolveNamedLocked(project, field, item, out)
		if err != nil {
			return nil, err
		}
		if n.ID != "" && seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) resolveNamedLocked(project, field string, item any, extra []model.Named) (model.Named, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return model.Named{}, FieldError{Field: field, Msg: "must be {id} and/or {name}"}
	}
	id := scalarID(m["id"])
	name, _ := m["name"].(string)
	if id == "" && name == "" {
		return model.Named{}, FieldError{Field: field, Msg: "must have id or name"}
	}
	cat := s.projectNamedCatalogLocked(project, field)
	lookup := append(append([]model.Named{}, cat...), extra...)
	if id != "" {
		for _, n := range lookup {
			if n.ID == id {
				return n, nil
			}
		}
		if len(cat) > 0 || name == "" {
			return model.Named{}, FieldError{Field: field, Msg: "unknown " + field + " id"}
		}
		return model.Named{ID: id, Name: name}, nil
	}
	for _, n := range lookup {
		if n.Name == name {
			return n, nil
		}
	}
	// A miss by name mints (gadak GDK-678, 2026-08-23): the catalog is
	// derived from issues, so "not in the catalog yet" is the normal state
	// of every new version/component — before this, only the first one
	// (empty catalog) could be created by name and the second 400ed.
	// A miss by id above still refuses: an id is a pointer, not a request.
	return model.Named{ID: slugID(name), Name: name}, nil
}

func (s *Store) projectNamedCatalogLocked(project, field string) []model.Named {
	seen := map[string]model.Named{}
	for _, iss := range s.allIssuesLocked() {
		if project != "" && iss.ProjectKey != project {
			continue
		}
		var list []model.Named
		switch field {
		case "fixVersions":
			list = iss.FixVersions
		case "components":
			list = iss.Components
		}
		for _, n := range list {
			if n.ID == "" {
				continue
			}
			if _, ok := seen[n.ID]; !ok {
				seen[n.ID] = n
			}
		}
	}
	out := make([]model.Named, 0, len(seen))
	for _, n := range seen {
		out = append(out, n)
	}
	return out
}

// ProjectVersions is GET /project/{key}/versions: the issue-derived
// {id,name} catalog for the project, sorted by name then id.
func (s *Store) ProjectVersions(projectKey string) []model.Named {
	return s.projectNamedCatalog(projectKey, "fixVersions")
}

// ProjectComponents is GET /project/{key}/components: the issue-derived
// {id,name} catalog for the project, sorted by name then id.
func (s *Store) ProjectComponents(projectKey string) []model.Named {
	return s.projectNamedCatalog(projectKey, "components")
}

func (s *Store) projectNamedCatalog(project, field string) []model.Named {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.projectNamedCatalogLocked(project, field)
	sortNamed(out)
	return out
}

func sortNamed(in []model.Named) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Name != in[j].Name {
			return in[i].Name < in[j].Name
		}
		return in[i].ID < in[j].ID
	})
}

func namedHasID(in []model.Named, id string) bool {
	for _, n := range in {
		if n.ID == id {
			return true
		}
	}
	return false
}

func namedRemoveID(in []model.Named, id string) []model.Named {
	if len(in) == 0 {
		return in
	}
	out := in[:0]
	for _, n := range in {
		if n.ID != id {
			out = append(out, n)
		}
	}
	return out
}

func asAnySlice(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case []map[string]any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = t[i]
		}
		return out, true
	}
	return nil, false
}

func scalarID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return ""
}

func (s *Store) setDesc(iss *model.Issue, v any) {
	switch t := v.(type) {
	case string:
		iss.DescriptionText = t
		iss.DescriptionADF = adf.Doc(t)
	default:
		b, _ := jsonMarshal(v)
		iss.DescriptionADF = b
		iss.DescriptionText = adf.Plain(b)
	}
}

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// CreateIssue files a new issue. fields is the Jira fields object;
// reporterID is the acting user used when fields omits reporter (gadak
// GDK-588 — an explicit fields.reporter always wins).
func (s *Store) CreateIssue(fields map[string]any, reporterID string) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := pickKey(fields["project"])
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	summary, _ := fields["summary"].(string)
	if strings.TrimSpace(summary) == "" {
		return nil, FieldError{Field: "summary", Msg: "You must specify a summary of the issue."}
	}
	if s.projectByKeyLocked(project) == nil {
		s.putProject(fixtures.Project{Key: project, Name: project})
	}
	typeID := first(pickID(fields["issuetype"]), "10003")
	parentKey, err := s.resolveParentLocked(typeID, pickKey(fields["parent"]))
	if err != nil {
		return nil, parentFieldError(err, parentCreateKeys)
	}
	s.seqIssue++
	n := s.nextKeyNum(project)
	key := fmt.Sprintf("%s-%d", project, n)
	now := clock.Format(s.clk.Tick())
	iss := &model.Issue{
		ID: strconv.Itoa(10000 + s.seqIssue), Key: key, Summary: summary,
		ProjectKey:  project,
		IssueTypeID: typeID,
		StatusID:    "10000",
		PriorityID:  first(pickID(fields["priority"]), "3"),
		AssigneeID:  pickID(fields["assignee"]),
		ReporterID:  pickID(fields["reporter"]),
		Created:     now, Updated: now,
		Labels:    stringSlice(fields["labels"]),
		ParentKey: parentKey,
	}
	if iss.ReporterID == "" {
		iss.ReporterID = s.userOrDefault(reporterID).AccountID
	}
	iss.CreatorID = iss.ReporterID
	s.setDesc(iss, fields["description"])
	if _, ok := fields["duedate"]; ok {
		if err := setDueDate(iss, fields["duedate"]); err != nil {
			return nil, err
		}
	}
	if v, ok := fields["fixVersions"]; ok {
		named, err := s.resolveNamedListLocked(project, "fixVersions", v)
		if err != nil {
			return nil, err
		}
		iss.FixVersions = named
	}
	if v, ok := fields["components"]; ok {
		named, err := s.resolveNamedListLocked(project, "components", v)
		if err != nil {
			return nil, err
		}
		iss.Components = named
	}
	known := map[string]bool{
		"project": true, "summary": true, "issuetype": true, "priority": true,
		"assignee": true, "reporter": true, "labels": true, "parent": true,
		"description": true, "duedate": true,
		"fixVersions": true, "components": true,
	}
	for k, v := range fields {
		if known[k] {
			continue
		}
		if err := s.validateCustomWriteLocked(k, v); err != nil {
			return nil, err
		}
		if iss.Custom == nil {
			iss.Custom = map[string]any{}
		}
		iss.Custom[k] = v
	}
	s.putIssueLocked(iss)
	return iss, s.markDirtyLocked()
}

func (s *Store) nextKeyNum(project string) int {
	max := 0
	prefix := project + "-"
	for _, k := range s.issueKeysLocked() {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(k, prefix))
		if err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

// AddAttachment stores bytes on an issue.
func (s *Store) AddAttachment(key, filename, mime, authorID string, body []byte) (model.Attachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return model.Attachment{}, errNotFound("issue", key)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	s.seqAttach++
	id := strconv.Itoa(70000 + s.seqAttach)
	s.putAttachBytesLocked(id, body)
	a := model.Attachment{
		ID: id, Filename: filename, MimeType: mime, Size: int64(len(body)),
		Author: *s.userOrDefault(authorID), Created: clock.Format(s.clk.Tick()),
		MediaID: uuid5(id),
	}
	iss.Attachments = append(iss.Attachments, a)
	iss.Updated = a.Created
	s.putIssueLocked(iss)
	return a, s.markDirtyLocked()
}

func pickID(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if id, ok := t["accountId"].(string); ok {
			return id
		}
		if id, ok := t["id"].(string); ok {
			return id
		}
		if id, ok := t["key"].(string); ok {
			return id
		}
		if id, ok := t["name"].(string); ok {
			return id
		}
	}
	return ""
}

func pickKey(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if k, ok := t["key"].(string); ok {
			return k
		}
		if k, ok := t["id"].(string); ok {
			return k
		}
	}
	return ""
}

// parentCreateKeys / parentEditKeys are Cloud's errors-map keys for an
// illegal parent, measured on a live site 2026-08-21. Create reports both
// parent and parentId; edit reports pid only.
var (
	parentCreateKeys = []string{"parent", "parentId"}
	parentEditKeys   = []string{"pid"}
)

func parentFieldError(err error, keys []string) FieldError {
	fe := FieldError{Field: keys[0], Msg: err.Error()}
	if len(keys) > 1 {
		fe.Also = append([]string{}, keys[1:]...)
	}
	return fe
}

func validParentLevel(parentLevel, childLevel int) bool {
	return parentLevel == childLevel+1
}

func (s *Store) issueByParentRefLocked(ref string) *model.Issue {
	return s.issueLocked(ref)
}

func (s *Store) typeNameAtLevelLocked(level int) string {
	var best *model.IssueType
	for _, t := range s.typesLocked() {
		if t.HierarchyLevel != level {
			continue
		}
		if best == nil || t.ID < best.ID {
			best = t
		}
	}
	if best == nil {
		return fmt.Sprintf("hierarchyLevel %d", level)
	}
	return best.Name
}

// resolveParentLocked is the single parent-hierarchy judgment used by
// CreateIssue and UpdateIssue. An empty ref clears the parent. A present
// ref must name an existing issue whose type.hierarchyLevel is exactly one
// above the child's. Names are used only in the error text.
func (s *Store) resolveParentLocked(childTypeID, parentRef string) (string, error) {
	if parentRef == "" {
		return "", nil
	}
	parent := s.issueByParentRefLocked(parentRef)
	if parent == nil {
		return "", fmt.Errorf("parent %s does not exist", parentRef)
	}
	childType := s.typeByIDLocked(childTypeID)
	if childType == nil {
		return "", fmt.Errorf("unknown issue type %s", childTypeID)
	}
	parentType := s.typeByIDLocked(parent.IssueTypeID)
	if parentType == nil {
		return "", fmt.Errorf("parent %s has unknown issue type %s", parent.Key, parent.IssueTypeID)
	}
	if !validParentLevel(parentType.HierarchyLevel, childType.HierarchyLevel) {
		want := s.typeNameAtLevelLocked(childType.HierarchyLevel + 1)
		return "", fmt.Errorf("%s cannot be a parent of %s; a parent must be one level above (%s)", parentType.Name, childType.Name, want)
	}
	return parent.Key, nil
}

// InvalidParentCount reports how many snapshot issues name a parent that
// is missing or not exactly one hierarchyLevel above the child. Load does
// not rewrite those links; this count is for diagnostics.
func InvalidParentCount(doc fixtures.Doc) int {
	types := map[string]fixtures.IssueType{}
	for _, t := range doc.IssueTypes {
		types[t.ID] = t
		if t.Name != "" {
			types[t.Name] = t
		}
	}
	byKey := map[string]fixtures.Issue{}
	for _, iss := range doc.Issues {
		byKey[iss.Key] = iss
	}
	n := 0
	for _, iss := range doc.Issues {
		if iss.Parent == "" {
			continue
		}
		parent, ok := byKey[iss.Parent]
		if !ok {
			n++
			continue
		}
		childT, cok := types[iss.Type]
		parentT, pok := types[parent.Type]
		if !cok || !pok || !validParentLevel(parentT.HierarchyLevel, childT.HierarchyLevel) {
			n++
		}
	}
	return n
}

func stringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string{}, t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

type notFoundError struct{ kind, id, display string }

func (e notFoundError) Error() string {
	if e.display != "" {
		return e.display
	}
	return e.kind + " " + e.id + " not found"
}

func errNotFound(kind, id string) error { return notFoundError{kind: kind, id: id} }

func errUnknownLinkType(ref string, byName bool) error {
	if byName {
		return notFoundError{
			kind:    "issue link type",
			id:      ref,
			display: "No issue link type with name '" + ref + "' found.",
		}
	}
	return notFoundError{
		kind:    "issue link type",
		id:      ref,
		display: "No issue link type with id '" + ref + "' found.",
	}
}

// NotFoundKind is the resource kind inside an IsNotFound error ("issue",
// "issue link type", …). Empty when err is not a not-found.
func NotFoundKind(err error) string {
	e, ok := err.(notFoundError)
	if !ok {
		return ""
	}
	return e.kind
}

// FieldError is a per-field write rejection (Jira's errors map).
type FieldError struct {
	Field string
	Msg   string
	// Also are extra errors-map keys that carry the same Msg. Create-parent
	// rejections set this to ["parentId"]; most field errors leave it empty.
	Also []string
}

func (e FieldError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

// Map is the Jira errors object for this rejection.
func (e FieldError) Map() map[string]string {
	m := map[string]string{}
	if e.Field != "" {
		m[e.Field] = e.Msg
	}
	for _, k := range e.Also {
		if k != "" {
			m[k] = e.Msg
		}
	}
	return m
}

// AsFieldError unwraps a FieldError.
func AsFieldError(err error) (FieldError, bool) {
	var fe FieldError
	if errors.As(err, &fe) {
		return fe, true
	}
	return FieldError{}, false
}

// PersistError is a durable-mode (PersistDebounce < 0) flush failure.
// The in-memory mutation has already been applied; a retry (caller or
// persist backoff) will try the disk write again.
type PersistError struct {
	Err error
}

func (e PersistError) Error() string {
	if e.Err == nil {
		return "persist failed; the change is in memory and a retry will try to write it again"
	}
	return fmt.Sprintf("persist: %v; the change is in memory and a retry will try to write it again", e.Err)
}

func (e PersistError) Unwrap() error { return e.Err }

// IsPersist reports a durable persist-flush failure.
func IsPersist(err error) bool {
	var pe PersistError
	return errors.As(err, &pe)
}

// IsNotFound reports a missing resource.
func IsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
}

type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func errConflict(msg string) error { return conflictError{msg} }

// IsConflict reports a write-vs-write conflict: a Confluence
// optimistic-concurrency miss (PUT version.number != current+1), or a
// Claim on an issue another actor already holds (gadak GDK-591).
func IsConflict(err error) bool {
	_, ok := err.(conflictError)
	return ok
}

// Snapshot returns a fixture document of the current graph.
func (s *Store) Snapshot() fixtures.Doc {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

// snapshotLocked is Snapshot without re-locking, for the persistence path
// which already holds the write lock.
func (s *Store) snapshotLocked() fixtures.Doc {
	d := fixtures.Doc{Seed: s.seed, Locale: string(s.loc)}
	for _, u := range s.usersLocked() {
		active := u.Active
		d.Users = append(d.Users, fixtures.User{
			AccountID: u.AccountID, Name: u.Name, Key: u.Key,
			DisplayName: u.DisplayName, Email: u.Email, Active: &active, TimeZone: u.TimeZone,
			AccountType: u.AccountType,
		})
	}
	sort.Slice(d.Users, func(i, j int) bool { return d.Users[i].AccountID < d.Users[j].AccountID })
	for _, p := range s.projectsLocked() {
		d.Projects = append(d.Projects, fixtures.Project{
			ID: p.ID, Key: p.Key, Name: p.Name, TypeKey: p.TypeKey, Style: p.Style,
		})
	}
	sort.Slice(d.Projects, func(i, j int) bool { return d.Projects[i].Key < d.Projects[j].Key })
	for _, st := range s.statusesLocked() {
		d.Statuses = append(d.Statuses, fixtures.Status{ID: st.ID, Name: st.Name, Category: st.StatusCategory.Key})
	}
	sort.Slice(d.Statuses, func(i, j int) bool { return d.Statuses[i].ID < d.Statuses[j].ID })
	for _, p := range s.prioritiesLocked() {
		d.Priorities = append(d.Priorities, fixtures.Priority{ID: p.ID, Name: p.Name})
	}
	for _, t := range s.typesLocked() {
		d.IssueTypes = append(d.IssueTypes, fixtures.IssueType{
			ID: t.ID, Name: t.Name, HierarchyLevel: t.HierarchyLevel, Subtask: t.Subtask,
		})
	}
	sort.Slice(d.IssueTypes, func(i, j int) bool { return d.IssueTypes[i].ID < d.IssueTypes[j].ID })
	d.Fields = fixtureFieldsFromStore(s.fieldsLocked())
	sort.Slice(d.Fields, func(i, j int) bool { return d.Fields[i].ID < d.Fields[j].ID })
	for _, id := range s.transitionScreenIDsLocked() {
		src, ok := s.transitionScreenLocked(id)
		if !ok {
			continue
		}
		fields := make(map[string]fixtures.TransitionScreenField, len(src))
		for k, v := range src {
			fields[k] = v
		}
		d.TransitionScreens = append(d.TransitionScreens, fixtures.TransitionScreen{
			Status: id, Fields: fields,
		})
	}
	for _, iss := range s.allIssuesLocked() {
		d.Issues = append(d.Issues, s.issueToFix(iss))
	}
	for _, sp := range s.spacesLocked() {
		d.Spaces = append(d.Spaces, fixtures.Space{
			ID: sp.ID, Key: sp.Key, Name: sp.Name, Type: sp.Type, Homepage: sp.HomepageID,
		})
	}
	sort.Slice(d.Spaces, func(i, j int) bool { return d.Spaces[i].Key < d.Spaces[j].Key })
	for _, p := range s.pagesLocked() {
		fp := fixtures.Page{
			ID: p.ID, Type: p.Type, Status: p.Status, Title: p.Title, Space: p.SpaceKey,
			Version: p.Version, When: p.When, Author: p.AuthorID, Body: p.BodyText, Labels: p.Labels,
		}
		if n := len(p.Ancestors); n > 0 {
			fp.Parent = p.Ancestors[n-1]
		}
		for _, v := range p.Versions {
			fp.Versions = append(fp.Versions, fixtures.PageVersion{
				Number: v.Number, When: v.When, Author: v.AuthorID,
				Message: v.Message, MinorEdit: v.MinorEdit,
			})
		}
		for _, cm := range s.pageCommentsLocked(p.ID) {
			fp.Comments = append(fp.Comments, fixtures.PageComment{
				ID: cm.ID, Author: cm.AuthorID, Body: cm.BodyText, When: cm.When,
			})
		}
		d.Pages = append(d.Pages, fp)
	}
	return d
}

// issueToFix includes attachment content: printable UTF-8 stays inline as
// `text` so the snapshot remains a document a person can read; anything
// binary goes out as `dataBase64`. Both load back through makeAttach.
func (s *Store) issueToFix(iss *model.Issue) fixtures.Issue {
	out := fixtures.Issue{
		ID: iss.ID, Key: iss.Key, Summary: iss.Summary,
		Description: iss.DescriptionText, Environment: iss.EnvironmentText,
		Type: iss.IssueTypeID, Status: iss.StatusID, Priority: iss.PriorityID,
		Assignee: iss.AssigneeID, Reporter: iss.ReporterID, Creator: iss.CreatorID,
		Project: iss.ProjectKey, Parent: iss.ParentKey, Labels: iss.Labels,
		Duedate: iss.Duedate, Resolution: iss.ResolutionID,
		Created: iss.Created, Updated: iss.Updated, Custom: iss.Custom,
	}
	for _, n := range iss.Components {
		out.Components = append(out.Components, n.Name)
	}
	for _, n := range iss.FixVersions {
		out.FixVersions = append(out.FixVersions, n.Name)
	}
	for _, n := range iss.Versions {
		out.Versions = append(out.Versions, n.Name)
	}
	for _, c := range iss.Comments {
		fc := fixtures.Comment{
			ID: c.ID, Author: c.Author.AccountID, Body: c.BodyText, Created: c.Created, Updated: c.Updated,
		}
		if c.Visibility != nil {
			fc.Visibility = &fixtures.CommentVisibility{Type: c.Visibility.Type, Value: c.Visibility.Value}
		}
		if c.JsdPublic != nil {
			internal := !*c.JsdPublic
			fc.Internal = &internal
		}
		out.Comments = append(out.Comments, fc)
	}
	for _, a := range iss.Attachments {
		fa := fixtures.Attachment{
			ID: a.ID, Filename: a.Filename, MimeType: a.MimeType,
			Author: a.Author.AccountID, Created: a.Created,
		}
		if body, ok := s.attachBytesLocked(a.ID); ok && len(body) > 0 {
			if text, readable := readableText(body); readable {
				fa.Text = text
			} else {
				fa.DataBase64 = base64.StdEncoding.EncodeToString(body)
			}
		}
		out.Attachments = append(out.Attachments, fa)
	}
	for _, l := range iss.Links {
		out.Links = append(out.Links, fixtures.Link{Type: l.TypeName, Inward: l.InwardKey, Outward: l.OutwardKey})
	}
	for _, pr := range iss.DevPRs {
		fpr := fixtures.DevPR{
			ID: pr.ID, URL: pr.URL, Name: pr.Name, Status: pr.Status, Updated: pr.Updated,
		}
		if pr.Author != nil {
			fpr.Author = pr.Author.Name
		}
		if pr.Source != nil {
			fpr.Branch = pr.Source.Branch
		}
		if pr.Actor != nil {
			fpr.ActorAccountID = pr.Actor.AccountID
			fpr.ActorDisplayName = pr.Actor.DisplayName
		}
		out.DevPRs = append(out.DevPRs, fpr)
	}
	for _, dep := range iss.DevDeployments {
		fdep := fixtures.Deployment{
			ID: dep.ID, URL: dep.URL, Environment: dep.Environment,
			State: dep.State, Updated: dep.Updated,
		}
		if dep.Actor != nil {
			fdep.ActorAccountID = dep.Actor.AccountID
			fdep.ActorDisplayName = dep.Actor.DisplayName
		}
		out.Deployments = append(out.Deployments, fdep)
	}
	for _, b := range iss.DevBuilds {
		fb := fixtures.Build{
			ID: b.ID, URL: b.URL, Number: b.Number, State: b.State, Updated: b.Updated,
		}
		if b.Actor != nil {
			fb.ActorAccountID = b.Actor.AccountID
			fb.ActorDisplayName = b.Actor.DisplayName
		}
		out.Builds = append(out.Builds, fb)
	}
	for _, h := range iss.Histories {
		fh := fixtures.History{ID: h.ID, At: h.Created, Author: h.Author.AccountID}
		for _, it := range h.Items {
			fh.Items = append(fh.Items, fixtures.HistoryItem{
				Field: it.Field, FieldID: it.FieldID,
				From: it.From, FromString: it.FromString, To: it.To, ToString: it.ToString,
			})
		}
		out.History = append(out.History, fh)
	}
	return out
}

// Counts is a dashboard summary.
func (s *Store) Counts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	comments := 0
	hist := 0
	for _, iss := range s.allIssuesLocked() {
		comments += len(iss.Comments)
		hist += len(iss.Histories)
	}
	return map[string]int{
		"projects": s.projectCountLocked(), "issues": s.issueCountLocked(),
		"comments": comments, "changelog": hist,
		"spaces": s.spaceCountLocked(), "pages": s.pageCountLocked(), "pageComments": s.pageCommentCountLocked(),
		"users": s.userCountLocked(), "statuses": s.statusCountLocked(),
	}
}
