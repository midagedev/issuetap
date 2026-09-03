// Package store is the deterministic Atlassian graph.
// Same fixture + same seed → same ids, timestamps, and ordering.
// The working copy is SQLite (JSON-blob tables). When PersistPath is
// set, that on-disk WAL database is the working copy; YAML is seed and
// Snapshot() export only.
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
	"slices"
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
	db        *sql.DB // :memory: or the PersistPath file (WAL)
	seed      int64
	clk       *clock.Clock
	wallClock bool
	loc       locale.Code
	tz        *time.Location
	tzName    string // fixture/persist timezone string; restored from store_meta

	// prioNamesEnglish is the embedded-role flag: a standalone workspace
	// is a real tracker, so priority names stay English under every
	// locale — what a live Cloud site does (gadak GDK-597).
	prioNamesEnglish bool

	// Sequence counters live in the working copy (store_meta "seq:*"
	// rows, nextSeqLocked), not in process memory: the persist is one
	// working copy shared by every process that opened it, and counters
	// seeded once at Open handed the same id to different issues
	// (gadak GDK-1180). Clock/locale/seed live in store_meta too.
	persist *persistState
}

// Options seed a store.
type Options struct {
	Seed   int64
	Locale locale.Code
	// PersistPath arms on-disk SQLite persistence (recommended .db).
	// When the file exists it is the working copy (fixture skipped by
	// embedders that honor load order). When it does not, Open creates
	// it and the caller seeds from a fixture. A non-SQLite file (legacy
	// YAML persist) is refused — pass that YAML as FixturePath.
	PersistPath string
	// PersistDebounce is retained for API compatibility and is a no-op:
	// every mutation commits before the call returns (durable-before-return
	// is the only mode).
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

// DefaultPersistDebounce is retained for API compatibility. PersistDebounce
// is a no-op; mutations commit before return.
const DefaultPersistDebounce = time.Second

// persistState names the on-disk DB. Mutations write SQL on s.db (the
// file itself); there is no YAML write-through and no debounce timer.
type persistState struct {
	path string
	err  error // last checkpoint error, if any
}

// New returns an empty store with default catalogs. When PersistPath is
// set, the working copy is that SQLite file (created if missing). Use
// Open to surface persist-file errors instead of panicking.
func New(opt Options) *Store {
	st, err := openStore(opt)
	if err != nil {
		panic("store: " + err.Error())
	}
	return st
}

// Open is New plus persist-file checks: a missing path creates a new DB,
// an existing SQLite DB is the initial graph, and a non-SQLite file
// (legacy YAML) is an error naming FixturePath. A corrupt file is an
// error, never a silent empty store.
func Open(opt Options) (*Store, error) {
	return openStore(opt)
}

func openStore(opt Options) (*Store, error) {
	if opt.Locale == "" {
		opt.Locale = locale.EN
	}
	clk := clock.New(opt.Seed)
	if opt.WallClock {
		clk = clock.NewWall()
	}
	s := &Store{
		seed:             opt.Seed,
		clk:              clk,
		wallClock:        opt.WallClock,
		loc:              opt.Locale,
		prioNamesEnglish: opt.PriorityNamesEnglish,
		tz:               time.FixedZone("KST", 9*3600),
	}
	if opt.PersistPath == "" {
		s.db = openWorkingDB()
		s.installDefaultCatalog()
		return s, nil
	}
	s.persist = &persistState{path: opt.PersistPath}
	_, statErr := os.Stat(opt.PersistPath)
	if statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("persist: stat %s: %w", opt.PersistPath, statErr)
		}
		db, err := createFileDB(opt.PersistPath)
		if err != nil {
			return nil, err
		}
		s.db = db
		s.installDefaultCatalog()
		s.writeMetaLocked()
		return s, nil
	}
	if err := inspectPersistPath(opt.PersistPath); err != nil {
		return nil, err
	}
	db, err := openExistingFileDB(opt.PersistPath)
	if err != nil {
		return nil, err
	}
	s.db = db
	s.loadMetaLocked()
	s.seedSeqsLocked()
	s.seedClockLocked()
	return s, nil
}

// markDirtyLocked records persist bookkeeping after a mutation. SQL
// statements have already auto-committed on the file-backed working
// copy, so ACK is durable. PersistDebounce is ignored.
func (s *Store) markDirtyLocked() error {
	if s.persist == nil {
		return nil
	}
	s.writeMetaLocked()
	return nil
}

func (s *Store) metaGetLocked(k string) (string, bool) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM store_meta WHERE k=?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		panic("store sqlite meta: " + err.Error())
	}
	return v, true
}

func (s *Store) writeMetaLocked() {
	s.sqlExec(`INSERT OR REPLACE INTO store_meta(k, v) VALUES(?,?)`, "seed", strconv.FormatInt(s.seed, 10))
	s.sqlExec(`INSERT OR REPLACE INTO store_meta(k, v) VALUES(?,?)`, "locale", string(s.loc))
	s.sqlExec(`INSERT OR REPLACE INTO store_meta(k, v) VALUES(?,?)`, "timezone", s.tzName)
}

func (s *Store) loadMetaLocked() {
	if v, ok := s.metaGetLocked("seed"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			s.seed = n
			if !s.wallClock {
				s.clk = clock.New(s.seed)
			}
		}
	}
	if v, ok := s.metaGetLocked("locale"); ok && v != "" {
		s.loc = locale.Parse(v)
	}
	if v, ok := s.metaGetLocked("timezone"); ok && v != "" {
		s.applyTimezoneLocked(v)
	}
}

func (s *Store) applyTimezoneLocked(name string) {
	s.tzName = name
	if name == "" {
		return
	}
	if loc, err := time.LoadLocation(name); err == nil {
		s.tz = loc
		return
	}
	if strings.HasPrefix(name, "+") || strings.HasPrefix(name, "-") {
		if t, err := time.Parse("-0700", name); err == nil {
			_, off := t.Zone()
			s.tz = time.FixedZone("fix", off)
		}
	}
}

// Flush checkpoints the WAL (no-op when persistence is not armed).
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.persist
	if p == nil {
		return nil
	}
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		p.err = err
		return err
	}
	p.err = nil
	return nil
}

// Close checkpoints WAL. The working copy stays readable after Close:
// persist tests (TestRestartDoesNotReuseIds) still Issue() the closed
// handle, matching the pre-cutover graph.
func (s *Store) Close() error {
	return s.Flush()
}

// PersistErr is the last checkpoint error, for embedders that poll disk
// health between flushes.
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
// entries of the same id/key, and issue/project/user maps are not wiped
// first. Catalogs are the exception: statuses, priorities and issue types
// are replaced wholesale by any fixture that declares them, and kept only
// when it declares none (GDK-1284).
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
		s.applyTimezoneLocked(doc.Timezone)
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
	// A fixture inherits the seeded catalog for anything it does not
	// mention — every shipped example leans on that, declaring Task and Bug
	// and letting Sub-task come from the defaults. What it must not inherit
	// is a default that answers to a name the fixture is now using for a
	// different id (GDK-1284): two entries under one name make every
	// name-keyed write ambiguous, and a migrated workspace landed with two
	// "Epic" types and two "In Progress" statuses, so `--type Epic` had no
	// answer at all. The fixture's id wins; the shadowed default goes.
	s.evictShadowedStatusesLocked(doc.Statuses)
	for _, st := range doc.Statuses {
		s.putStatus(st)
	}
	if len(doc.Priorities) > 0 {
		s.clearPrioritiesLocked()
		for i, p := range doc.Priorities {
			s.putPriorityLocked(&model.Priority{ID: p.ID, Name: p.Name, Rank: i})
		}
	}
	s.evictShadowedTypesLocked(doc.IssueTypes)
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
	maxC, maxA, maxH, maxR := 0, 0, 0, 0
	bumpComment := func(id string) {
		n, err := strconv.Atoi(id)
		if err != nil {
			return
		}
		// Runtime ids: issue comments 90000+seq, page comments 30000+seq
		// (both share the "comment" counter). Authored ids outside those bands are
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
		for _, rl := range iss.RemoteLinks {
			if n, err := strconv.Atoi(rl.ID); err == nil && n > 60000 && n < 70000 && n-60000 > maxR {
				maxR = n - 60000
			}
		}
	}
	for _, pid := range s.pageCommentParentIDsLocked() {
		for _, c := range s.pageCommentsLocked(pid) {
			bumpComment(c.ID)
		}
	}
	s.floorSeqLocked("comment", maxC)
	s.floorSeqLocked("attach", maxA)
	s.floorSeqLocked("hist", maxH)
	s.floorSeqLocked("remotelink", maxR)
	// Runtime space ids: 40000+seq. Old persists carry no "seq:space" row,
	// so a restart used to re-mint from zero into the occupied band.
	maxS := 0
	for _, sp := range s.spacesLocked() {
		if n, err := strconv.Atoi(sp.ID); err == nil && n > 40000 && n-40000 > maxS {
			maxS = n - 40000
		}
	}
	s.floorSeqLocked("space", maxS)
	// Runtime page ids: 20000+seq. Authored ids outside that band
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
	s.floorSeqLocked("page", maxP)
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

// seedIssueSeqLocked floors the "issue" counter from the highest existing numeric issue
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
		s.floorSeqLocked("issue", max-10000)
	}
}

func (s *Store) putUser(u fixtures.User) *model.User {
	id := u.AccountID
	if id == "" {
		id = fmt.Sprintf("5b10a2844c20165700ede%03d", s.nextSeqLocked("user"))
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
		id = strconv.Itoa(10000 + s.nextSeqLocked("proj"))
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
		id = strconv.Itoa(40000 + s.nextSeqLocked("space"))
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
		id = strconv.Itoa(10000 + s.nextSeqLocked("issue"))
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
	descADF, descText := fixtureBody(in.Description, in.DescriptionADF)
	iss := &model.Issue{
		ID: id, Key: in.Key, Summary: in.Summary,
		DescriptionText: descText, DescriptionADF: descADF,
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
	for _, rl := range in.RemoteLinks {
		id := rl.ID
		if id == "" {
			id = strconv.Itoa(60000 + s.nextSeqLocked("remotelink"))
		}
		iss.RemoteLinks = append(iss.RemoteLinks, model.RemoteLink{
			ID: id, GlobalID: rl.GlobalID, Relationship: rl.Relationship,
			URL: rl.URL, Title: first(rl.Title, rl.URL), Summary: rl.Summary,
		})
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
		id = strconv.Itoa(90000 + s.nextSeqLocked("comment"))
	}
	created := first(c.Created, fallback)
	updated := first(c.Updated, created)
	author := s.userOrDefault(c.Author)
	bodyADF, bodyText := fixtureBody(c.Body, c.BodyADF)
	cm := model.Comment{
		ID: id, Author: *author, Body: bodyADF, BodyText: bodyText,
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
		id = strconv.Itoa(70000 + s.nextSeqLocked("attach"))
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
		id = "h" + strconv.Itoa(s.nextSeqLocked("hist"))
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
	id := "h" + strconv.Itoa(s.nextSeqLocked("hist"))
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
		id = strconv.Itoa(20000 + s.nextSeqLocked("page"))
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
	bodyADF, bodyText := fixtureBody(p.Body, p.BodyADF)
	pg := &model.Page{
		ID: id, Type: typ, Status: st, Title: p.Title, SpaceKey: p.Space,
		Version: ver, When: when, AuthorID: authorID,
		BodyADF: bodyADF, BodyText: bodyText, BodyStorage: adf.StorageXHTML(bodyText),
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
			cid = strconv.Itoa(30000 + s.nextSeqLocked("comment"))
		}
		cwhen := first(c.When, when)
		parent := id
		if c.ReplyTo != "" {
			parent = c.ReplyTo
		}
		cADF, cText := fixtureBody(c.Body, c.BodyADF)
		s.appendPageCommentLocked(parent, model.PageComment{
			ID: cid, Title: "Re: " + p.Title, ParentID: parent,
			BodyADF: cADF, BodyText: cText, Version: 1, When: cwhen,
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
// Direction follows real Jira, which labels each issue's projection by the
// OTHER end's POST role: the issue posted as outwardIssue gets an
// inwardIssue element (and so displays type.inward), the issue posted as
// inwardIssue gets an outwardIssue element (type.outward). Measured on
// Cloud with blocks and split from (gadak GDK-1204, gadak PR #79); until
// then this store held the mirror image and standalone read the opposite
// phrase from the one Cloud showed for the same POST.
//
// Duplicate handling: the same catalog type + same outward key + same
// inward key is a successful no-op. HTTP still returns 201, but a second
// issuelinks element is not appended — a gadak link retry then cannot grow
// the mirror on re-read. A one-sided fixture row is healed by writing the
// missing side only; fixture rows are authored in this same Jira
// convention.
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
	if !issueHasDirectedLink(outward, lt.Name, false, inward.Key) {
		outward.Links = append(outward.Links, model.IssueLink{TypeName: lt.Name, InwardKey: inward.Key})
		added = true
	}
	if !issueHasDirectedLink(inward, lt.Name, true, outward.Key) {
		inward.Links = append(inward.Links, model.IssueLink{TypeName: lt.Name, OutwardKey: outward.Key})
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

// DeleteIssueLink removes the link typeID:outwardKey:inwardKey — both
// projections, whichever convention wrote them. A pair written under the
// pre-GDK-1204 mirror-image convention is byte-identical to the corrected
// pair for the reversed intent, so the same two removals cover old persist
// rows without a migration. Removing nothing is a not-found, like Cloud.
func (s *Store) DeleteIssueLink(typeID, outwardKey, inwardKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lt, err := lookupIssueLinkType(typeID, "")
	if err != nil {
		return err
	}
	outward := s.issueByParentRefLocked(outwardKey)
	inward := s.issueByParentRefLocked(inwardKey)
	if outward == nil || inward == nil {
		return errNotFound("issueLink", issueLinkRef(typeID, outwardKey, inwardKey))
	}
	removed := false
	outward.Links = slices.DeleteFunc(outward.Links, func(l model.IssueLink) bool {
		hit := strings.EqualFold(l.TypeName, lt.Name) && l.InwardKey == inward.Key
		removed = removed || hit
		return hit
	})
	inward.Links = slices.DeleteFunc(inward.Links, func(l model.IssueLink) bool {
		hit := strings.EqualFold(l.TypeName, lt.Name) && l.OutwardKey == outward.Key
		removed = removed || hit
		return hit
	})
	if !removed {
		return errNotFound("issueLink", issueLinkRef(typeID, outwardKey, inwardKey))
	}
	now := clock.Format(s.clk.Tick())
	outward.Updated = now
	inward.Updated = now
	s.putIssueLocked(outward)
	s.putIssueLocked(inward)
	return s.markDirtyLocked()
}

// RemoteLinks lists key's remote issue links (gadak GDK-1032).
func (s *Store) RemoteLinks(key string) ([]model.RemoteLink, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return nil, errNotFound("issue", key)
	}
	return append([]model.RemoteLink{}, iss.RemoteLinks...), nil
}

// UpsertRemoteLink creates or updates one remote link on key. A non-empty
// GlobalID is the upsert identity — Cloud's contract; without one every
// call creates. Returns the stored row and whether it was created.
func (s *Store) UpsertRemoteLink(key string, rl model.RemoteLink) (model.RemoteLink, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return model.RemoteLink{}, false, errNotFound("issue", key)
	}
	if strings.TrimSpace(rl.URL) == "" {
		return model.RemoteLink{}, false, fmt.Errorf("object.url is required")
	}
	if rl.Title == "" {
		rl.Title = rl.URL
	}
	if rl.GlobalID != "" {
		for i := range iss.RemoteLinks {
			if iss.RemoteLinks[i].GlobalID == rl.GlobalID {
				rl.ID = iss.RemoteLinks[i].ID
				iss.RemoteLinks[i] = rl
				iss.Updated = clock.Format(s.clk.Tick())
				s.putIssueLocked(iss)
				return rl, false, s.markDirtyLocked()
			}
		}
	}
	rl.ID = strconv.Itoa(60000 + s.nextSeqLocked("remotelink"))
	iss.RemoteLinks = append(iss.RemoteLinks, rl)
	iss.Updated = clock.Format(s.clk.Tick())
	s.putIssueLocked(iss)
	return rl, true, s.markDirtyLocked()
}

// DeleteRemoteLink removes one remote link by its id.
func (s *Store) DeleteRemoteLink(key, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return errNotFound("issue", key)
	}
	kept := iss.RemoteLinks[:0]
	removed := false
	for _, rl := range iss.RemoteLinks {
		if rl.ID == id {
			removed = true
			continue
		}
		kept = append(kept, rl)
	}
	if !removed {
		return errNotFound("remoteLink", id)
	}
	iss.RemoteLinks = kept
	iss.Updated = clock.Format(s.clk.Tick())
	s.putIssueLocked(iss)
	return s.markDirtyLocked()
}

func issueLinkRef(typeID, outwardKey, inwardKey string) string {
	return typeID + ":" + outwardKey + ":" + inwardKey
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
	id := strconv.Itoa(20000 + s.nextSeqLocked("page"))
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
	s.appendHistoryLocked(iss, authorID, []model.HistoryItem{{
		Field: "status", FieldID: "status",
		From: from, FromString: s.displayFor("status", from),
		To: to, ToString: s.displayFor("status", to),
	}})
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
	now := clock.Format(s.clk.Tick())
	cm := model.Comment{
		ID:         strconv.Itoa(90000 + s.nextSeqLocked("comment")),
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
	cm := model.PageComment{
		ID: strconv.Itoa(30000 + s.nextSeqLocked("comment")), Title: "Re: " + pg.Title,
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
// in-progress transition (gadak GDK-591). A no-change write records no
// row, like Cloud (gadak GDK-1208).
func (s *Store) setAssigneeLocked(iss *model.Issue, accountID, authorID string) {
	from := iss.AssigneeID
	iss.AssigneeID = accountID
	iss.Updated = clock.Format(s.clk.Tick())
	if from == accountID {
		return
	}
	s.appendHistoryLocked(iss, authorID, []model.HistoryItem{{
		Field: "assignee", FieldID: "assignee",
		From: from, FromString: s.displayFor("assignee", from),
		To: accountID, ToString: s.displayFor("assignee", accountID),
	}})
}

// appendHistoryLocked writes one changelog group. Created matches
// iss.Updated, which the caller has already ticked.
func (s *Store) appendHistoryLocked(iss *model.Issue, authorID string, items []model.HistoryItem) {
	iss.Histories = append(iss.Histories, model.History{
		ID: "h" + strconv.Itoa(s.nextSeqLocked("hist")), Created: iss.Updated,
		Author: *s.userOrDefault(authorID),
		Items:  items,
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
	return s.UpdateIssue(key, fields, nil, "")
}

// UpdateIssue applies Jira Cloud PUT /issue {fields, update}.
// update is processed first, then fields (Cloud order). Unsupported
// update fields return an error instead of a silent no-op.
// fixVersions and components are first-class typed arrays (not Custom);
// fields replaces the whole list, update accepts add/remove/set.
// authorID is the acting user recorded on the changelog group; every
// field the request actually changed gets one history item, a no-change
// write gets none (gadak GDK-1208).
func (s *Store) UpdateIssue(key string, fields, update map[string]any, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issueByKeyLocked(key)
	if iss == nil {
		return errNotFound("issue", key)
	}
	// Sorted so the changelog item order is deterministic (same seed →
	// same snapshot is a repo contract; Go map order is not).
	touched := make([]string, 0, len(fields)+len(update))
	for k := range fields {
		touched = append(touched, k)
	}
	for k := range update {
		if _, dup := fields[k]; !dup {
			touched = append(touched, k)
		}
	}
	sort.Strings(touched)
	before := make(map[string][2]string, len(touched))
	for _, k := range touched {
		raw, disp := s.changeSnapshotLocked(iss, k)
		before[k] = [2]string{raw, disp}
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
	var items []model.HistoryItem
	for _, k := range touched {
		raw, disp := s.changeSnapshotLocked(iss, k)
		if b := before[k]; b[0] == raw && b[1] == disp {
			continue
		}
		b := before[k]
		items = append(items, model.HistoryItem{
			Field: k, FieldID: k,
			From: b[0], FromString: b[1],
			To: raw, ToString: disp,
		})
	}
	if len(items) > 0 {
		s.appendHistoryLocked(iss, authorID, items)
	}
	s.putIssueLocked(iss)
	return s.markDirtyLocked()
}

// changeSnapshotLocked is one editable field reduced to a comparable
// (raw id, display) pair — the from/fromString half of a changelog item
// before the write, the to/toString half after it. Fields without a
// catalog id (text, lists) carry "" raw and join names with a space,
// Cloud's labels convention.
func (s *Store) changeSnapshotLocked(iss *model.Issue, k string) (raw, display string) {
	switch k {
	case "summary":
		return "", iss.Summary
	case "description":
		return "", iss.DescriptionText
	case "labels":
		return "", strings.Join(iss.Labels, " ")
	case "priority":
		return iss.PriorityID, s.displayFor("priority", iss.PriorityID)
	case "issuetype":
		return iss.IssueTypeID, s.displayFor("issuetype", iss.IssueTypeID)
	case "parent":
		return iss.ParentKey, iss.ParentKey
	case "duedate":
		return iss.Duedate, iss.Duedate
	case "assignee":
		return iss.AssigneeID, s.displayFor("assignee", iss.AssigneeID)
	case "fixVersions":
		return "", joinNamed(iss.FixVersions)
	case "components":
		return "", joinNamed(iss.Components)
	default:
		return "", customValueString(iss.Custom[k])
	}
}

func joinNamed(in []model.Named) string {
	names := make([]string, 0, len(in))
	for _, n := range in {
		names = append(names, n.Name)
	}
	return strings.Join(names, " ")
}

func customValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
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

// fixtureBody resolves a fixture body from its two slots. The ADF slot wins
// when it parses as a document — a migration from another tracker carrying
// the origin's formatting (gadak GDK-1382), or a Snapshot of a body a person
// formatted here; the text beside it is derived when the fixture left it
// empty so search and the DC dialect still see words. Otherwise the plain
// text is wrapped by adf.Doc as fixtures always were.
func fixtureBody(text, adfRaw string) (json.RawMessage, string) {
	if strings.TrimSpace(adfRaw) != "" {
		if doc, plain, err := parseADF(json.RawMessage(adfRaw)); err == nil {
			if text == "" {
				text = plain
			}
			return doc, text
		}
	}
	return adf.Doc(text), text
}

// fixtureADF is the export half of fixtureBody: the ADF slot Snapshot writes.
// Empty when the document is exactly what adf.Doc(text) rebuilds, so a plain
// body stays a plain body in the snapshot and only formatting the text
// cannot hold is written — which is what fixtureBody then prefers on load.
func fixtureADF(doc json.RawMessage, text string) string {
	if len(doc) == 0 || string(doc) == string(adf.Doc(text)) {
		return ""
	}
	return string(doc)
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
	issueID := strconv.Itoa(10000 + s.nextSeqLocked("issue"))
	n := s.nextKeyNum(project)
	key := fmt.Sprintf("%s-%d", project, n)
	now := clock.Format(s.clk.Tick())
	iss := &model.Issue{
		ID: issueID, Key: key, Summary: summary,
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
	id := strconv.Itoa(70000 + s.nextSeqLocked("attach"))
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

// PersistError is a persist-layer failure after the graph mutation has
// already been applied. Stage 3 commits SQL before return, so this is
// reserved for checkpoint / residual disk errors; YAML write-through
// no longer produces it.
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
			BodyADF: fixtureADF(p.BodyADF, p.BodyText),
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
				ID: cm.ID, Author: cm.AuthorID, Body: cm.BodyText, BodyADF: fixtureADF(cm.BodyADF, cm.BodyText),
				When: cm.When,
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
		Description: iss.DescriptionText, DescriptionADF: fixtureADF(iss.DescriptionADF, iss.DescriptionText),
		Environment: iss.EnvironmentText,
		Type:        iss.IssueTypeID, Status: iss.StatusID, Priority: iss.PriorityID,
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
			ID: c.ID, Author: c.Author.AccountID, Body: c.BodyText, BodyADF: fixtureADF(c.Body, c.BodyText),
			Created: c.Created, Updated: c.Updated,
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
	for _, rl := range iss.RemoteLinks {
		out.RemoteLinks = append(out.RemoteLinks, fixtures.RemoteLink{
			ID: rl.ID, GlobalID: rl.GlobalID, Relationship: rl.Relationship,
			URL: rl.URL, Title: rl.Title, Summary: rl.Summary,
		})
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
