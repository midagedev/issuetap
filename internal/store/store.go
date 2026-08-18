// Package store is the in-memory, deterministic Atlassian graph.
// Same fixture + same seed → same ids, timestamps, and ordering.
// There is no database. Snapshot/restore is a fixture document.
package store

import (
	"crypto/sha1"
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
	mu   sync.RWMutex
	seed int64
	clk  *clock.Clock
	loc  locale.Code
	tz   *time.Location

	seqIssue   int
	seqComment int
	seqHist    int
	seqAttach  int
	seqPage    int
	seqUser    int
	seqProj    int
	seqSpace   int

	users        map[string]*model.User // accountId or name
	usersByEmail map[string]*model.User
	projects     map[string]*model.Project
	statuses     map[string]*model.Status
	priorities   []*model.Priority
	prioByID     map[string]*model.Priority
	types        map[string]*model.IssueType
	resolutions  map[string]*model.Resolution
	fields       []model.FieldInfo
	filters      []model.Filter
	issues       map[string]*model.Issue
	spaces       map[string]*model.Space
	pages        map[string]*model.Page
	pageComments map[string][]model.PageComment // parent content id
	attachBytes  map[string][]byte
	persist      *persistState
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
	// mutation.
	PersistDebounce time.Duration
}

// DefaultPersistDebounce is the write-through quiet window.
const DefaultPersistDebounce = time.Second

// persistState is the write-through engine. Owned by Store.mu.
type persistState struct {
	path     string
	debounce time.Duration
	timer    *time.Timer
	dirty    bool
	err      error // last write error; cleared by the next success
}

// New returns an empty store with default catalogs. When PersistPath is
// set, mutations are persisted; use Open to also reload an existing file.
func New(opt Options) *Store {
	if opt.Locale == "" {
		opt.Locale = locale.EN
	}
	s := &Store{
		seed:         opt.Seed,
		clk:          clock.New(opt.Seed),
		loc:          opt.Locale,
		tz:           time.FixedZone("KST", 9*3600),
		users:        map[string]*model.User{},
		usersByEmail: map[string]*model.User{},
		projects:     map[string]*model.Project{},
		statuses:     map[string]*model.Status{},
		prioByID:     map[string]*model.Priority{},
		types:        map[string]*model.IssueType{},
		resolutions:  map[string]*model.Resolution{},
		issues:       map[string]*model.Issue{},
		spaces:       map[string]*model.Space{},
		pages:        map[string]*model.Page{},
		pageComments: map[string][]model.PageComment{},
		attachBytes:  map[string][]byte{},
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
		return nil, fmt.Errorf("persist: apply %s: %w", opt.PersistPath, err)
	}
	st.mu.Lock()
	if st.persist.timer != nil { // the load armed the debounce; disarm it
		st.persist.timer.Stop()
		st.persist.timer = nil
	}
	st.persist.dirty = false // the load itself is not a mutation
	st.mu.Unlock()
	return st, nil
}

// markDirtyLocked arms the debounced write. Called by every mutation.
func (s *Store) markDirtyLocked() {
	p := s.persist
	if p == nil {
		return
	}
	p.dirty = true
	if p.timer != nil {
		p.timer.Reset(p.debounce)
		return
	}
	p.timer = time.AfterFunc(p.debounce, s.flushPersist)
}

// flushPersist is the timer callback: one atomic write when dirty.
func (s *Store) flushPersist() {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.persist
	if p == nil {
		return
	}
	p.timer = nil
	if !p.dirty {
		return
	}
	if err := s.writePersistLocked(); err != nil {
		p.err = err // stays dirty; retried by the next mutation or Close
		return
	}
	p.err = nil
	p.dirty = false
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
// persistence is not armed) and returns the write error, if any.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.persist
	if p == nil {
		return nil
	}
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	if !p.dirty {
		return nil
	}
	if err := s.writePersistLocked(); err != nil {
		p.err = err
		return err
	}
	p.err = nil
	p.dirty = false
	return nil
}

// Close flushes pending mutations and stops the debounce timer. Safe to
// call on a store without persistence.
func (s *Store) Close() error {
	return s.Flush()
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
		s.statuses[id] = &model.Status{
			ID: id, Name: name,
			StatusCategory: model.StatusCategory{
				ID: model.CategoryID(cat), Key: cat,
				Name: name, ColorName: model.CategoryColor(cat),
			},
		}
	}
	addS("10000", "To Do", "new")
	addS("3", "In Progress", "indeterminate")
	addS("10003", "Done", "done")

	s.priorities = []*model.Priority{
		{ID: "1", Name: "Highest", StatusColor: "#d04437", Rank: 0},
		{ID: "2", Name: "High", StatusColor: "#f15C75", Rank: 1},
		{ID: "3", Name: "Medium", StatusColor: "#f79232", Rank: 2},
		{ID: "4", Name: "Low", StatusColor: "#707070", Rank: 3},
		{ID: "5", Name: "Lowest", StatusColor: "#999999", Rank: 4},
	}
	for _, p := range s.priorities {
		s.prioByID[p.ID] = p
	}

	addT := func(id, name string, hier int, sub bool) {
		s.types[id] = &model.IssueType{ID: id, Name: name, HierarchyLevel: hier, Subtask: sub}
	}
	addT("10000", "Epic", 1, false)
	addT("10003", "Task", 0, false)
	addT("10007", "Bug", 0, false)
	addT("10004", "Story", 0, false)
	addT("10002", "Sub-task", -1, true)

	s.resolutions["10000"] = &model.Resolution{ID: "10000", Name: "Done"}
	s.resolutions["10001"] = &model.Resolution{ID: "10001", Name: "Won't Do"}
	s.resolutions["10002"] = &model.Resolution{ID: "10002", Name: "Duplicate"}
	s.resolutions["10003"] = &model.Resolution{ID: "10003", Name: "Cannot Reproduce"}

	s.fields = defaultFields()
}

func defaultFields() []model.FieldInfo {
	sys := []struct{ id, typ string }{
		{"issuetype", "issuetype"}, {"project", "project"}, {"status", "status"},
		{"priority", "priority"}, {"assignee", "user"}, {"reporter", "user"},
		{"summary", "string"}, {"description", "string"}, {"comment", "comments-page"},
		{"labels", "array"}, {"components", "array"}, {"fixVersions", "array"},
		{"created", "datetime"}, {"updated", "datetime"}, {"resolution", "resolution"},
		{"environment", "string"}, {"statusCategory", "statusCategory"}, {"parent", "issuelink"},
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
func (s *Store) SetLocale(c locale.Code) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loc = c
	s.markDirtyLocked()
}

// Seed is the determinism seed.
func (s *Store) Seed() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.seed
}

// Apply replaces the graph with a fixture. Catalog defaults remain and are
// overwritten by fixture rows of the same id.
func (s *Store) Apply(doc fixtures.Doc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if doc.Seed != 0 {
		s.seed = doc.Seed
		s.clk = clock.New(doc.Seed)
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
	if len(s.users) == 0 {
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
		s.priorities = s.priorities[:0]
		s.prioByID = map[string]*model.Priority{}
		for i, p := range doc.Priorities {
			pp := &model.Priority{ID: p.ID, Name: p.Name, Rank: i}
			s.priorities = append(s.priorities, pp)
			s.prioByID[p.ID] = pp
		}
	}
	for _, t := range doc.IssueTypes {
		s.types[t.ID] = &model.IssueType{
			ID: t.ID, Name: t.Name, HierarchyLevel: t.HierarchyLevel, Subtask: t.Subtask,
		}
	}
	for _, r := range doc.Resolutions {
		s.resolutions[r.ID] = &model.Resolution{ID: r.ID, Name: r.Name}
	}
	for _, f := range doc.Fields {
		s.fields = append(s.fields, model.FieldInfo{
			ID: f.ID, Key: f.ID, Name: f.Name, Custom: f.Custom,
			Schema:    model.FieldSchema{Type: f.Type},
			Orderable: true, Navigable: true, Searchable: true,
		})
	}
	for _, f := range doc.Filters {
		s.filters = append(s.filters, model.Filter{
			ID: f.ID, Name: f.Name, JQL: f.JQL, Favourite: f.Favourite, Owner: f.Owner,
		})
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
	s.markDirtyLocked()
	return nil
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
	for _, iss := range s.issues {
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
	for _, cms := range s.pageComments {
		for _, c := range cms {
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
	for id := range s.pages {
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
	for _, iss := range s.issues {
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
	for _, p := range s.pages {
		see(p.When)
		for _, v := range p.Versions {
			see(v.When)
		}
		for _, c := range s.pageComments[p.ID] {
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
	for _, iss := range s.issues {
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
		TimeZone: tz, AccountType: "atlassian",
		AvatarURLs: avatarURLs(id),
	}
	s.users[id] = m
	if m.Name != "" {
		s.users[m.Name] = m
	}
	if m.Email != "" {
		s.usersByEmail[strings.ToLower(m.Email)] = m
	}
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
	s.projects[p.Key] = &model.Project{
		ID: id, Key: p.Key, Name: p.Name, TypeKey: typ, Style: style,
		Simplified: style == "next-gen",
	}
}

func (s *Store) putStatus(st fixtures.Status) {
	cat := st.Category
	if cat == "" {
		cat = "new"
	}
	s.statuses[st.ID] = &model.Status{
		ID: st.ID, Name: st.Name,
		StatusCategory: model.StatusCategory{
			ID: model.CategoryID(cat), Key: cat,
			Name: st.Name, ColorName: model.CategoryColor(cat),
		},
	}
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
	s.spaces[p.Key] = &model.Space{
		ID: id, Key: p.Key, Name: p.Name, Type: typ, Status: "current",
		HomepageID: p.Homepage,
	}
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
	if _, ok := s.projects[project]; !ok {
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
	s.issues[iss.Key] = iss
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
	return model.Comment{
		ID: id, Author: *author, Body: adf.Doc(c.Body), BodyText: c.Body,
		Created: created, Updated: updated,
	}
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
	s.attachBytes[id] = body
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
	for id, st := range s.statuses {
		if st.StatusCategory.Key == cat {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	return s.statuses[ids[0]]
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
	s.pages[id] = pg
	if sp := s.spaces[p.Space]; sp != nil && sp.HomepageID == "" {
		sp.HomepageID = id
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
		s.pageComments[parent] = append(s.pageComments[parent], model.PageComment{
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
	if u, ok := s.users[ref]; ok {
		return u.AccountID
	}
	if u, ok := s.usersByEmail[strings.ToLower(ref)]; ok {
		return u.AccountID
	}
	for _, u := range s.users {
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
		if u := s.users[id]; u != nil {
			cp := *u
			return &cp
		}
	}
	for _, u := range s.users {
		cp := *u
		return &cp
	}
	u := s.putUser(fixtures.User{
		AccountID: "5b10a2844c20165700ede21g", DisplayName: "Ada Lovelace",
		Email: "you@example.com",
	})
	cp := *u
	return &cp
}

func (s *Store) resolveType(ref string) string {
	if ref == "" {
		return "10003" // Task
	}
	if t, ok := s.types[ref]; ok {
		return t.ID
	}
	for _, t := range s.types {
		if strings.EqualFold(t.Name, ref) {
			return t.ID
		}
	}
	// Create so a fixture can introduce a site-specific type.
	s.types[ref] = &model.IssueType{ID: ref, Name: ref}
	return ref
}

func (s *Store) resolveStatus(ref string) string {
	if ref == "" {
		return "10000"
	}
	if st, ok := s.statuses[ref]; ok {
		return st.ID
	}
	for _, st := range s.statuses {
		if strings.EqualFold(st.Name, ref) {
			return st.ID
		}
	}
	s.statuses[ref] = &model.Status{
		ID: ref, Name: ref,
		StatusCategory: model.StatusCategory{ID: 2, Key: "new", Name: ref, ColorName: "blue-gray"},
	}
	return ref
}

func (s *Store) resolvePriority(ref string) string {
	if ref == "" {
		return "3"
	}
	if p, ok := s.prioByID[ref]; ok {
		return p.ID
	}
	for _, p := range s.priorities {
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
	if r, ok := s.resolutions[ref]; ok {
		return r.ID
	}
	for _, r := range s.resolutions {
		if strings.EqualFold(r.Name, ref) {
			return r.ID
		}
	}
	return ref
}

func (s *Store) displayFor(fieldID, id string) string {
	switch fieldID {
	case "status":
		if st := s.statuses[id]; st != nil {
			return st.Name
		}
	case "priority":
		if p := s.prioByID[id]; p != nil {
			return p.Name
		}
	case "issuetype":
		if t := s.types[id]; t != nil {
			return t.Name
		}
	case "assignee", "reporter":
		if u := s.users[id]; u != nil {
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

// Lookup is the JQL resolver.
func (s *Store) Lookup() jql.Lookup {
	return jql.Lookup{
		Status: func(id string) *model.Status {
			if st := s.statuses[id]; st != nil {
				cp := locale.OverlayStatus(s.loc, *st)
				return &cp
			}
			return nil
		},
		IssueType: func(id string) *model.IssueType {
			if t := s.types[id]; t != nil {
				cp := locale.OverlayIssueType(s.loc, *t)
				return &cp
			}
			return nil
		},
		Priority: func(id string) *model.Priority {
			if p := s.prioByID[id]; p != nil {
				cp := locale.OverlayPriority(s.loc, *p)
				return &cp
			}
			return nil
		},
		User: func(id string) *model.User {
			if u := s.users[id]; u != nil {
				cp := *u
				return &cp
			}
			return nil
		},
		Location: s.tz,
	}
}

// AllIssues returns a snapshot copy of issue pointers (do not mutate).
func (s *Store) allIssuesLocked() []*model.Issue {
	out := make([]*model.Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		out = append(out, iss)
	}
	return out
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
	if iss, ok := s.issues[keyOrID]; ok {
		return iss
	}
	for _, iss := range s.issues {
		if iss.ID == keyOrID {
			return iss
		}
	}
	return nil
}

// Projects lists projects ordered by key.
func (s *Store) Projects() []*model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*model.Project, 0, len(s.projects))
	for _, p := range s.projects {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Project by key.
func (s *Store) Project(key string) *model.Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.projects[key]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// Statuses lists every status (localized copies).
func (s *Store) Statuses() []model.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.statuses))
	for id := range s.statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.Status, 0, len(ids))
	for _, id := range ids {
		out = append(out, locale.OverlayStatus(s.loc, *s.statuses[id]))
	}
	return out
}

// Status by id.
func (s *Store) Status(id string) *model.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := s.statuses[id]
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
	out := make([]model.Priority, 0, len(s.priorities))
	for _, p := range s.priorities {
		out = append(out, locale.OverlayPriority(s.loc, *p))
	}
	return out
}

// Priority by id.
func (s *Store) Priority(id string) *model.Priority {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := s.prioByID[id]
	if p == nil {
		return nil
	}
	cp := locale.OverlayPriority(s.loc, *p)
	return &cp
}

// IssueTypes lists types.
func (s *Store) IssueTypes() []model.IssueType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.types))
	for id := range s.types {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.IssueType, 0, len(ids))
	for _, id := range ids {
		out = append(out, locale.OverlayIssueType(s.loc, *s.types[id]))
	}
	return out
}

// IssueType by id.
func (s *Store) IssueType(id string) *model.IssueType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.types[id]
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
	ids := make([]string, 0, len(s.resolutions))
	for id := range s.resolutions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]model.Resolution, 0, len(ids))
	for _, id := range ids {
		out = append(out, locale.OverlayResolution(s.loc, *s.resolutions[id]))
	}
	return out
}

// Fields is the field catalog with localized names.
func (s *Store) Fields() []model.FieldInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.FieldInfo, 0, len(s.fields))
	for _, f := range s.fields {
		out = append(out, locale.OverlayField(s.loc, f))
	}
	return out
}

// Filters is GET /filter/my.
func (s *Store) Filters() []model.Filter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]model.Filter{}, s.filters...)
	return out
}

// Users lists unique users.
func (s *Store) Users() []model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var out []model.User
	for _, u := range s.users {
		if seen[u.AccountID] {
			continue
		}
		seen[u.AccountID] = true
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out
}

// User by accountId, username, or email.
func (s *Store) User(ref string) *model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u := s.users[ref]; u != nil {
		cp := *u
		return &cp
	}
	if u := s.usersByEmail[strings.ToLower(ref)]; u != nil {
		cp := *u
		return &cp
	}
	return nil
}

// UserByEmail looks up the login email.
func (s *Store) UserByEmail(email string) *model.User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if u := s.usersByEmail[strings.ToLower(email)]; u != nil {
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
	seen := map[string]bool{}
	for _, u := range s.users {
		if seen[u.AccountID] {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(u.DisplayName), q) ||
			strings.Contains(strings.ToLower(u.Email), q) ||
			strings.Contains(strings.ToLower(u.Name), q) {
			seen[u.AccountID] = true
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
	for _, sp := range s.spaces {
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
	sp := s.spaces[key]
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
	p := s.pages[id]
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
	out := make([]model.Page, 0, len(s.pages))
	for _, p := range s.pages {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ChildComments lists comments on a content id.
func (s *Store) ChildComments(contentID string) []model.PageComment {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.PageComment{}, s.pageComments[contentID]...)
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
		for pid, cms := range s.pageComments {
			pg := s.pages[pid]
			space := ""
			if pg != nil {
				space = pg.SpaceKey
			}
			for _, cm := range cms {
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
		for _, p := range s.pages {
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
	if in.SpaceKey == "" || s.spaces[in.SpaceKey] == nil {
		return nil, fmt.Errorf("No space found with key: %s", in.SpaceKey)
	}
	var ancestors []string
	if in.ParentID != "" {
		parent := s.pages[in.ParentID]
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
	s.pages[id] = pg
	s.markDirtyLocked()
	cp := *pg
	return &cp, nil
}

// UpdatePage applies a Confluence optimistic-concurrency update: Next must
// equal the current version + 1. A miss is IsConflict (HTTP 409).
func (s *Store) UpdatePage(id string, in PageWrite) (*model.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pg := s.pages[id]
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
	if in.SpaceKey != "" && s.spaces[in.SpaceKey] == nil {
		return nil, fmt.Errorf("No space found with key: %s", in.SpaceKey)
	}
	if in.ParentID != "" {
		if in.ParentID != pg.ID && s.pages[in.ParentID] == nil {
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
	s.markDirtyLocked()
	cp := *pg
	return &cp, nil
}

// PageVersions returns history newest-first (number descending). That is
// the Cloud v2 default and the documented order for this endpoint.
func (s *Store) PageVersions(id string) ([]model.PageVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pg := s.pages[id]
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
	b := s.attachBytes[id]
	for _, iss := range s.issues {
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
	for _, iss := range s.issues {
		for i := range iss.Attachments {
			if iss.Attachments[i].MediaID == media {
				a := iss.Attachments[i]
				return s.attachBytes[a.ID], &a
			}
		}
	}
	return nil, nil
}

// Transitions for an issue: every other status in the catalog. This is a
// model, not a workflow engine.
func (s *Store) Transitions(key string) []model.Transition {
	s.mu.RLock()
	defer s.mu.RUnlock()
	iss := s.issues[key]
	if iss == nil {
		for _, it := range s.issues {
			if it.ID == key {
				iss = it
				break
			}
		}
	}
	if iss == nil {
		return nil
	}
	ids := make([]string, 0, len(s.statuses))
	for id := range s.statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []model.Transition
	n := 1
	for _, id := range ids {
		if id == iss.StatusID {
			continue
		}
		st := locale.OverlayStatus(s.loc, *s.statuses[id])
		out = append(out, model.Transition{
			ID: strconv.Itoa(n), Name: st.Name, ToID: id,
		})
		n++
	}
	return out
}

// Transition applies a transition id (the synthetic id from Transitions).
func (s *Store) Transition(key, transitionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issues[key]
	if iss == nil {
		return errNotFound("issue", key)
	}
	// Recompute transitions under the lock.
	ids := make([]string, 0, len(s.statuses))
	for id := range s.statuses {
		if id != iss.StatusID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	idx, err := strconv.Atoi(transitionID)
	if err != nil || idx < 1 || idx > len(ids) {
		return errNotFound("transition", transitionID)
	}
	to := ids[idx-1]
	from := iss.StatusID
	iss.StatusID = to
	iss.Updated = clock.Format(s.clk.Tick())
	if s.statuses[to] != nil && s.statuses[to].StatusCategory.Key == "done" && iss.ResolutionID == "" {
		iss.ResolutionID = "10000"
	}
	if s.statuses[to] != nil && s.statuses[to].StatusCategory.Key != "done" {
		iss.ResolutionID = ""
	}
	s.seqHist++
	iss.Histories = append(iss.Histories, model.History{
		ID: "h" + strconv.Itoa(s.seqHist), Created: iss.Updated,
		Author: *s.userOrDefault(""),
		Items: []model.HistoryItem{{
			Field: "status", FieldID: "status",
			From: from, FromString: s.displayFor("status", from),
			To: to, ToString: s.displayFor("status", to),
		}},
	})
	s.markDirtyLocked()
	return nil
}

// AddComment posts a comment. body is ADF or a string.
func (s *Store) AddComment(key, authorID string, body []byte) (model.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issues[key]
	if iss == nil {
		return model.Comment{}, errNotFound("issue", key)
	}
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
		ID:     strconv.Itoa(90000 + s.seqComment),
		Author: *s.userOrDefault(authorID),
		Body:   body, BodyText: text, Created: now, Updated: now,
	}
	iss.Comments = append(iss.Comments, cm)
	iss.Updated = now
	s.markDirtyLocked()
	return cm, nil
}

// SetAssignee assigns or unassigns.
func (s *Store) SetAssignee(key, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issues[key]
	if iss == nil {
		return errNotFound("issue", key)
	}
	from := iss.AssigneeID
	iss.AssigneeID = accountID
	iss.Updated = clock.Format(s.clk.Tick())
	s.seqHist++
	iss.Histories = append(iss.Histories, model.History{
		ID: "h" + strconv.Itoa(s.seqHist), Created: iss.Updated,
		Author: *s.userOrDefault(""),
		Items: []model.HistoryItem{{
			Field: "assignee", FieldID: "assignee",
			From: from, FromString: s.displayFor("assignee", from),
			To: accountID, ToString: s.displayFor("assignee", accountID),
		}},
	})
	s.markDirtyLocked()
	return nil
}

// UpdateFields applies a fields map (summary, description, labels, …).
func (s *Store) UpdateFields(key string, fields map[string]any) error {
	return s.UpdateIssue(key, fields, nil)
}

// UpdateIssue applies Jira Cloud PUT /issue {fields, update}.
// update is processed first, then fields (Cloud order). Unsupported
// update fields return an error instead of a silent no-op.
func (s *Store) UpdateIssue(key string, fields, update map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss := s.issues[key]
	if iss == nil {
		return errNotFound("issue", key)
	}
	if err := applyUpdateOps(iss, update); err != nil {
		return err
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
			iss.ParentKey = pickKey(v)
		default:
			if iss.Custom == nil {
				iss.Custom = map[string]any{}
			}
			iss.Custom[k] = v
		}
	}
	iss.Updated = clock.Format(s.clk.Tick())
	s.markDirtyLocked()
	return nil
}

func applyUpdateOps(iss *model.Issue, update map[string]any) error {
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

// CreateIssue files a new issue. fields is the Jira fields object.
func (s *Store) CreateIssue(fields map[string]any) (*model.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	project := pickKey(fields["project"])
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if _, ok := s.projects[project]; !ok {
		s.putProject(fixtures.Project{Key: project, Name: project})
	}
	summary, _ := fields["summary"].(string)
	s.seqIssue++
	n := s.nextKeyNum(project)
	key := fmt.Sprintf("%s-%d", project, n)
	now := clock.Format(s.clk.Tick())
	iss := &model.Issue{
		ID: strconv.Itoa(10000 + s.seqIssue), Key: key, Summary: summary,
		ProjectKey:  project,
		IssueTypeID: first(pickID(fields["issuetype"]), "10003"),
		StatusID:    "10000",
		PriorityID:  first(pickID(fields["priority"]), "3"),
		AssigneeID:  pickID(fields["assignee"]),
		ReporterID:  pickID(fields["reporter"]),
		Created:     now, Updated: now,
		Labels:    stringSlice(fields["labels"]),
		ParentKey: pickKey(fields["parent"]),
	}
	if iss.ReporterID == "" {
		iss.ReporterID = s.userOrDefault("").AccountID
	}
	iss.CreatorID = iss.ReporterID
	s.setDesc(iss, fields["description"])
	known := map[string]bool{
		"project": true, "summary": true, "issuetype": true, "priority": true,
		"assignee": true, "reporter": true, "labels": true, "parent": true,
		"description": true,
	}
	for k, v := range fields {
		if known[k] {
			continue
		}
		if iss.Custom == nil {
			iss.Custom = map[string]any{}
		}
		iss.Custom[k] = v
	}
	s.issues[key] = iss
	s.markDirtyLocked()
	return iss, nil
}

func (s *Store) nextKeyNum(project string) int {
	max := 0
	prefix := project + "-"
	for k := range s.issues {
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
	iss := s.issues[key]
	if iss == nil {
		return model.Attachment{}, errNotFound("issue", key)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	s.seqAttach++
	id := strconv.Itoa(70000 + s.seqAttach)
	s.attachBytes[id] = body
	a := model.Attachment{
		ID: id, Filename: filename, MimeType: mime, Size: int64(len(body)),
		Author: *s.userOrDefault(authorID), Created: clock.Format(s.clk.Tick()),
		MediaID: uuid5(id),
	}
	iss.Attachments = append(iss.Attachments, a)
	iss.Updated = a.Created
	s.markDirtyLocked()
	return a, nil
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

type notFoundError struct{ kind, id string }

func (e notFoundError) Error() string { return e.kind + " " + e.id + " not found" }

func errNotFound(kind, id string) error { return notFoundError{kind, id} }

// IsNotFound reports a missing resource.
func IsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
}

type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func errConflict(msg string) error { return conflictError{msg} }

// IsConflict reports a Confluence optimistic-concurrency miss
// (PUT version.number != current+1).
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
	seenU := map[string]bool{}
	for _, u := range s.users {
		if seenU[u.AccountID] {
			continue
		}
		seenU[u.AccountID] = true
		active := u.Active
		d.Users = append(d.Users, fixtures.User{
			AccountID: u.AccountID, Name: u.Name, Key: u.Key,
			DisplayName: u.DisplayName, Email: u.Email, Active: &active, TimeZone: u.TimeZone,
		})
	}
	sort.Slice(d.Users, func(i, j int) bool { return d.Users[i].AccountID < d.Users[j].AccountID })
	for _, p := range s.projects {
		d.Projects = append(d.Projects, fixtures.Project{
			ID: p.ID, Key: p.Key, Name: p.Name, TypeKey: p.TypeKey, Style: p.Style,
		})
	}
	sort.Slice(d.Projects, func(i, j int) bool { return d.Projects[i].Key < d.Projects[j].Key })
	for _, st := range s.statuses {
		d.Statuses = append(d.Statuses, fixtures.Status{ID: st.ID, Name: st.Name, Category: st.StatusCategory.Key})
	}
	sort.Slice(d.Statuses, func(i, j int) bool { return d.Statuses[i].ID < d.Statuses[j].ID })
	for _, p := range s.priorities {
		d.Priorities = append(d.Priorities, fixtures.Priority{ID: p.ID, Name: p.Name})
	}
	for _, t := range s.types {
		d.IssueTypes = append(d.IssueTypes, fixtures.IssueType{
			ID: t.ID, Name: t.Name, HierarchyLevel: t.HierarchyLevel, Subtask: t.Subtask,
		})
	}
	sort.Slice(d.IssueTypes, func(i, j int) bool { return d.IssueTypes[i].ID < d.IssueTypes[j].ID })
	keys := make([]string, 0, len(s.issues))
	for k := range s.issues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.Issues = append(d.Issues, s.issueToFix(s.issues[k]))
	}
	for _, sp := range s.spaces {
		d.Spaces = append(d.Spaces, fixtures.Space{
			ID: sp.ID, Key: sp.Key, Name: sp.Name, Type: sp.Type, Homepage: sp.HomepageID,
		})
	}
	sort.Slice(d.Spaces, func(i, j int) bool { return d.Spaces[i].Key < d.Spaces[j].Key })
	pids := make([]string, 0, len(s.pages))
	for id := range s.pages {
		pids = append(pids, id)
	}
	sort.Strings(pids)
	for _, id := range pids {
		p := s.pages[id]
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
		for _, cm := range s.pageComments[id] {
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
		out.Comments = append(out.Comments, fixtures.Comment{
			ID: c.ID, Author: c.Author.AccountID, Body: c.BodyText, Created: c.Created, Updated: c.Updated,
		})
	}
	for _, a := range iss.Attachments {
		fa := fixtures.Attachment{
			ID: a.ID, Filename: a.Filename, MimeType: a.MimeType,
			Author: a.Author.AccountID, Created: a.Created,
		}
		if body, ok := s.attachBytes[a.ID]; ok && len(body) > 0 {
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
	for _, iss := range s.issues {
		comments += len(iss.Comments)
		hist += len(iss.Histories)
	}
	pages := len(s.pages)
	pcom := 0
	for _, c := range s.pageComments {
		pcom += len(c)
	}
	users := 0
	seen := map[string]bool{}
	for _, u := range s.users {
		if !seen[u.AccountID] {
			seen[u.AccountID] = true
			users++
		}
	}
	return map[string]int{
		"projects": len(s.projects), "issues": len(s.issues),
		"comments": comments, "changelog": hist,
		"spaces": len(s.spaces), "pages": pages, "pageComments": pcom,
		"users": users, "statuses": len(s.statuses),
	}
}
