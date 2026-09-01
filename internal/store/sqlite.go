// SQLite is the store graph (F′ stage 3 / gadak GDK-202). PersistPath
// names an on-disk WAL database; that file is the working copy. YAML is
// the seed and Snapshot() export format, not the durable write path.
// Without PersistPath the working copy stays process-local (:memory:).
//
// Blobs are JSON with wrappers for model fields that carry `json:"-"`
// (issue links, attachment MediaID, FieldInfo.Options, Filter.Owner,
// Priority.Rank). gob is not used: it collapses a pointer to the zero
// value (*bool false on Comment.JsdPublic) to nil.
//
// persistSchemaVersion is PRAGMA user_version. It is persist bookkeeping,
// not entity normalization: a mismatch is refused, never migrated in
// this round. store_meta holds seed/locale/timezone so a restart restores
// them without a YAML document.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
	"github.com/midagedev/issuetap/internal/model"

	_ "modernc.org/sqlite"
)

// persistSchemaVersion is this binary's on-disk schema stamp
// (PRAGMA user_version). Bump only with a migration; this round refuses
// any other value.
const persistSchemaVersion = 1

const sqliteMagic = "SQLite format 3\x00"

// memSeq names each :memory: database so two Store values never share a
// working copy. cache=shared keeps the unique name stable across the
// single connection MaxOpenConns(1) holds open.
var memSeq atomic.Uint64

const workingSchema = `
CREATE TABLE users (
  account_id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  blob BLOB NOT NULL
);
CREATE INDEX users_name ON users(name);
CREATE INDEX users_email ON users(email);

CREATE TABLE projects (
  key TEXT PRIMARY KEY,
  id TEXT NOT NULL,
  blob BLOB NOT NULL
);
CREATE INDEX projects_id ON projects(id);

CREATE TABLE statuses (
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE priorities (
  id TEXT PRIMARY KEY,
  rank INTEGER NOT NULL,
  blob BLOB NOT NULL
);

CREATE TABLE issue_types (
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE resolutions (
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE fields (
  id TEXT PRIMARY KEY,
  ord INTEGER NOT NULL,
  blob BLOB NOT NULL
);

CREATE TABLE filters (
  ord INTEGER PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE transition_screens (
  status_id TEXT PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE issues (
  key TEXT PRIMARY KEY,
  id TEXT NOT NULL,
  blob BLOB NOT NULL
);
CREATE INDEX issues_id ON issues(id);

CREATE TABLE spaces (
  key TEXT PRIMARY KEY,
  id TEXT NOT NULL,
  blob BLOB NOT NULL
);

CREATE TABLE pages (
  id TEXT PRIMARY KEY,
  blob BLOB NOT NULL
);

CREATE TABLE page_comments (
  parent_id TEXT NOT NULL,
  ord INTEGER NOT NULL,
  blob BLOB NOT NULL,
  PRIMARY KEY (parent_id, ord)
);

CREATE TABLE attachments (
  id TEXT PRIMARY KEY,
  bytes BLOB NOT NULL
);

CREATE TABLE store_meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
`

func openWorkingDB() *sql.DB {
	name := fmt.Sprintf("file:issuetap-%d?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", memSeq.Add(1))
	db, err := sql.Open("sqlite", name)
	if err != nil {
		panic("store sqlite open: " + err.Error())
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		panic("store sqlite ping: " + err.Error())
	}
	if _, err := db.Exec(workingSchema); err != nil {
		panic("store sqlite schema: " + err.Error())
	}
	return db
}

// persistDSN is the on-disk Open DSN. Matches the gadak mirror family:
// WAL, busy_timeout(5000), foreign_keys, synchronous=NORMAL, immediate
// transactions so a read-then-write cannot upgrade a deferred lock.
func persistDSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file:" + abs + "?" + strings.Join([]string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")
}

func persistYAMLError(path string) error {
	return fmt.Errorf("persist %s: file is not an issuetap SQLite state database; if this is a legacy YAML persist file, pass it as FixturePath and set PersistPath to a new .db file", path)
}

func persistSchemaError(path string, have, want int) error {
	return fmt.Errorf("persist %s: schema_version %d (this build reads %d)", path, have, want)
}

func isSQLiteFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var hdr [16]byte
	n, err := io.ReadFull(f, hdr[:])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if n < 16 {
		return false, nil
	}
	return string(hdr[:]) == sqliteMagic, nil
}

func inspectPersistPath(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("persist %s: is a directory", path)
	}
	ok, err := isSQLiteFile(path)
	if err != nil {
		return fmt.Errorf("persist %s: %w", path, err)
	}
	if !ok {
		return persistYAMLError(path)
	}
	return nil
}

func configureFileDB(db *sql.DB) {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
}

func createFileDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", persistDSN(path))
	if err != nil {
		return nil, fmt.Errorf("persist: open %s: %w", path, err)
	}
	configureFileDB(db)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: ping %s: %w", path, err)
	}
	if _, err := db.Exec(workingSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: schema %s: %w", path, err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", persistSchemaVersion)); err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: user_version %s: %w", path, err)
	}
	return db, nil
}

func openExistingFileDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", persistDSN(path))
	if err != nil {
		return nil, fmt.Errorf("persist: open %s: %w", path, err)
	}
	configureFileDB(db)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: ping %s: %w", path, err)
	}
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: schema_version %s: %w", path, err)
	}
	if have != persistSchemaVersion {
		db.Close()
		return nil, persistSchemaError(path, have, persistSchemaVersion)
	}
	var tbl string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='issues'`).Scan(&tbl)
	if err == sql.ErrNoRows {
		db.Close()
		return nil, persistSchemaError(path, have, persistSchemaVersion)
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("persist: inspect %s: %w", path, err)
	}
	return db, nil
}

func jsonEncode(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("store sqlite json encode: " + err.Error())
	}
	return b
}

func jsonDecode[T any](b []byte) T {
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		panic("store sqlite json decode: " + err.Error())
	}
	return v
}

// storedIssue is the JSON form of model.Issue. Several model fields carry
// `json:"-"` (links, attachment MediaID); encoding the model type itself
// would drop them. gob is not a substitute: it collapses a pointer to the
// zero value (*bool false on Comment.JsdPublic) to nil.
type storedIssue struct {
	ID              string                `json:"id"`
	Key             string                `json:"key"`
	Summary         string                `json:"summary"`
	DescriptionADF  []byte                `json:"descriptionAdf"`
	DescriptionText string                `json:"descriptionText"`
	EnvironmentADF  []byte                `json:"environmentAdf"`
	EnvironmentText string                `json:"environmentText"`
	IssueTypeID     string                `json:"issueTypeId"`
	StatusID        string                `json:"statusId"`
	PriorityID      string                `json:"priorityId"`
	AssigneeID      string                `json:"assigneeId"`
	ReporterID      string                `json:"reporterId"`
	CreatorID       string                `json:"creatorId"`
	ProjectKey      string                `json:"projectKey"`
	ParentKey       string                `json:"parentKey"`
	Labels          []string              `json:"labels"`
	Components      []model.Named         `json:"components"`
	FixVersions     []model.Named         `json:"fixVersions"`
	Versions        []model.Named         `json:"versions"`
	Duedate         string                `json:"duedate"`
	ResolutionID    string                `json:"resolutionId"`
	Created         string                `json:"created"`
	Updated         string                `json:"updated"`
	Comments        []storedComment       `json:"comments"`
	Attachments     []storedAttachment    `json:"attachments"`
	DevPRs          []model.DevPR         `json:"devPrs"`
	DevDeployments  []model.DevDeployment `json:"devDeployments"`
	DevBuilds       []model.DevBuild      `json:"devBuilds"`
	RemoteLinks     []model.RemoteLink    `json:"remoteLinks,omitempty"`
	Links           []storedLink          `json:"links"`
	Histories       []model.History       `json:"histories"`
	Custom          map[string]any        `json:"custom"`
}

type storedComment struct {
	ID         string            `json:"id"`
	Author     model.User        `json:"author"`
	Body       []byte            `json:"body"`
	BodyText   string            `json:"bodyText"`
	Created    string            `json:"created"`
	Updated    string            `json:"updated"`
	Visibility *model.Visibility `json:"visibility"`
	JsdPublic  *bool             `json:"jsdPublic"`
}

type storedPage struct {
	ID          string              `json:"id"`
	Type        string              `json:"type"`
	Status      string              `json:"status"`
	Title       string              `json:"title"`
	SpaceKey    string              `json:"spaceKey"`
	Version     int                 `json:"version"`
	When        string              `json:"when"`
	AuthorID    string              `json:"authorId"`
	BodyADF     []byte              `json:"bodyAdf"`
	BodyText    string              `json:"bodyText"`
	BodyStorage string              `json:"bodyStorage"`
	Labels      []string            `json:"labels"`
	Ancestors   []string            `json:"ancestors"`
	Container   string              `json:"container"`
	WebUI       string              `json:"webui"`
	Versions    []model.PageVersion `json:"versions"`
}

type storedPageComment struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	ParentID string `json:"parentId"`
	BodyADF  []byte `json:"bodyAdf"`
	BodyText string `json:"bodyText"`
	Version  int    `json:"version"`
	When     string `json:"when"`
	AuthorID string `json:"authorId"`
}

type storedLink struct {
	TypeName   string `json:"typeName"`
	InwardKey  string `json:"inwardKey"`
	OutwardKey string `json:"outwardKey"`
}

type storedAttachment struct {
	ID       string     `json:"id"`
	Filename string     `json:"filename"`
	MimeType string     `json:"mimeType"`
	Size     int64      `json:"size"`
	Author   model.User `json:"author"`
	Created  string     `json:"created"`
	MediaID  string     `json:"mediaId"`
}

type storedField struct {
	ID         string              `json:"id"`
	Key        string              `json:"key"`
	Name       string              `json:"name"`
	Custom     bool                `json:"custom"`
	Clause     []string            `json:"clause"`
	Schema     model.FieldSchema   `json:"schema"`
	Orderable  bool                `json:"orderable"`
	Navigable  bool                `json:"navigable"`
	Searchable bool                `json:"searchable"`
	Options    []model.FieldOption `json:"options"`
}

type storedFilter struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	JQL       string `json:"jql"`
	Favourite bool   `json:"favourite"`
	Owner     string `json:"owner"`
}

type storedPriority struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	StatusColor string `json:"statusColor"`
	Rank        int    `json:"rank"`
}

func encodeIssue(iss *model.Issue) []byte {
	st := storedIssue{
		ID: iss.ID, Key: iss.Key, Summary: iss.Summary,
		DescriptionADF: []byte(iss.DescriptionADF), DescriptionText: iss.DescriptionText,
		EnvironmentADF: []byte(iss.EnvironmentADF), EnvironmentText: iss.EnvironmentText,
		IssueTypeID: iss.IssueTypeID, StatusID: iss.StatusID, PriorityID: iss.PriorityID,
		AssigneeID: iss.AssigneeID, ReporterID: iss.ReporterID, CreatorID: iss.CreatorID,
		ProjectKey: iss.ProjectKey, ParentKey: iss.ParentKey,
		Labels: iss.Labels, Components: iss.Components, FixVersions: iss.FixVersions,
		Versions: iss.Versions, Duedate: iss.Duedate, ResolutionID: iss.ResolutionID,
		Created: iss.Created, Updated: iss.Updated,
		DevPRs:         iss.DevPRs,
		DevDeployments: iss.DevDeployments, DevBuilds: iss.DevBuilds,
		RemoteLinks: iss.RemoteLinks,
		Histories:   iss.Histories, Custom: iss.Custom,
	}
	for _, c := range iss.Comments {
		st.Comments = append(st.Comments, storedComment{
			ID: c.ID, Author: c.Author, Body: []byte(c.Body), BodyText: c.BodyText,
			Created: c.Created, Updated: c.Updated, Visibility: c.Visibility, JsdPublic: c.JsdPublic,
		})
	}
	for _, a := range iss.Attachments {
		st.Attachments = append(st.Attachments, storedAttachment{
			ID: a.ID, Filename: a.Filename, MimeType: a.MimeType, Size: a.Size,
			Author: a.Author, Created: a.Created, MediaID: a.MediaID,
		})
	}
	for _, l := range iss.Links {
		st.Links = append(st.Links, storedLink{TypeName: l.TypeName, InwardKey: l.InwardKey, OutwardKey: l.OutwardKey})
	}
	return jsonEncode(st)
}

func decodeIssue(b []byte) model.Issue {
	st := jsonDecode[storedIssue](b)
	iss := model.Issue{
		ID: st.ID, Key: st.Key, Summary: st.Summary,
		DescriptionADF: json.RawMessage(st.DescriptionADF), DescriptionText: st.DescriptionText,
		EnvironmentADF: json.RawMessage(st.EnvironmentADF), EnvironmentText: st.EnvironmentText,
		IssueTypeID: st.IssueTypeID, StatusID: st.StatusID, PriorityID: st.PriorityID,
		AssigneeID: st.AssigneeID, ReporterID: st.ReporterID, CreatorID: st.CreatorID,
		ProjectKey: st.ProjectKey, ParentKey: st.ParentKey,
		Labels: st.Labels, Components: st.Components, FixVersions: st.FixVersions,
		Versions: st.Versions, Duedate: st.Duedate, ResolutionID: st.ResolutionID,
		Created: st.Created, Updated: st.Updated,
		DevPRs:         st.DevPRs,
		DevDeployments: st.DevDeployments, DevBuilds: st.DevBuilds,
		RemoteLinks: st.RemoteLinks,
		Histories:   st.Histories, Custom: st.Custom,
	}
	for _, c := range st.Comments {
		iss.Comments = append(iss.Comments, model.Comment{
			ID: c.ID, Author: c.Author, Body: json.RawMessage(c.Body), BodyText: c.BodyText,
			Created: c.Created, Updated: c.Updated, Visibility: c.Visibility, JsdPublic: c.JsdPublic,
		})
	}
	for _, a := range st.Attachments {
		media := a.MediaID
		if media == "" {
			media = uuid5(a.ID)
		}
		iss.Attachments = append(iss.Attachments, model.Attachment{
			ID: a.ID, Filename: a.Filename, MimeType: a.MimeType, Size: a.Size,
			Author: a.Author, Created: a.Created, MediaID: media,
		})
	}
	for _, l := range st.Links {
		iss.Links = append(iss.Links, model.IssueLink{TypeName: l.TypeName, InwardKey: l.InwardKey, OutwardKey: l.OutwardKey})
	}
	return iss
}

func encodeField(f model.FieldInfo) []byte {
	return jsonEncode(storedField{
		ID: f.ID, Key: f.Key, Name: f.Name, Custom: f.Custom, Clause: f.Clause,
		Schema: f.Schema, Orderable: f.Orderable, Navigable: f.Navigable,
		Searchable: f.Searchable, Options: f.Options,
	})
}

func decodeField(b []byte) model.FieldInfo {
	st := jsonDecode[storedField](b)
	return model.FieldInfo{
		ID: st.ID, Key: st.Key, Name: st.Name, Custom: st.Custom, Clause: st.Clause,
		Schema: st.Schema, Orderable: st.Orderable, Navigable: st.Navigable,
		Searchable: st.Searchable, Options: st.Options,
	}
}

func encodeFilter(f model.Filter) []byte {
	return jsonEncode(storedFilter{ID: f.ID, Name: f.Name, JQL: f.JQL, Favourite: f.Favourite, Owner: f.Owner})
}

func decodeFilter(b []byte) model.Filter {
	st := jsonDecode[storedFilter](b)
	return model.Filter{ID: st.ID, Name: st.Name, JQL: st.JQL, Favourite: st.Favourite, Owner: st.Owner}
}

func encodePage(p *model.Page) []byte {
	return jsonEncode(storedPage{
		ID: p.ID, Type: p.Type, Status: p.Status, Title: p.Title, SpaceKey: p.SpaceKey,
		Version: p.Version, When: p.When, AuthorID: p.AuthorID,
		BodyADF: []byte(p.BodyADF), BodyText: p.BodyText, BodyStorage: p.BodyStorage,
		Labels: p.Labels, Ancestors: p.Ancestors, Container: p.Container, WebUI: p.WebUI,
		Versions: p.Versions,
	})
}

func decodePage(b []byte) model.Page {
	st := jsonDecode[storedPage](b)
	return model.Page{
		ID: st.ID, Type: st.Type, Status: st.Status, Title: st.Title, SpaceKey: st.SpaceKey,
		Version: st.Version, When: st.When, AuthorID: st.AuthorID,
		BodyADF: json.RawMessage(st.BodyADF), BodyText: st.BodyText, BodyStorage: st.BodyStorage,
		Labels: st.Labels, Ancestors: st.Ancestors, Container: st.Container, WebUI: st.WebUI,
		Versions: st.Versions,
	}
}

func encodePageComment(cm model.PageComment) []byte {
	return jsonEncode(storedPageComment{
		ID: cm.ID, Title: cm.Title, ParentID: cm.ParentID,
		BodyADF: []byte(cm.BodyADF), BodyText: cm.BodyText,
		Version: cm.Version, When: cm.When, AuthorID: cm.AuthorID,
	})
}

func decodePageComment(b []byte) model.PageComment {
	st := jsonDecode[storedPageComment](b)
	return model.PageComment{
		ID: st.ID, Title: st.Title, ParentID: st.ParentID,
		BodyADF: json.RawMessage(st.BodyADF), BodyText: st.BodyText,
		Version: st.Version, When: st.When, AuthorID: st.AuthorID,
	}
}

func encodePriority(p *model.Priority) []byte {
	return jsonEncode(storedPriority{ID: p.ID, Name: p.Name, StatusColor: p.StatusColor, Rank: p.Rank})
}

func decodePriority(b []byte) model.Priority {
	st := jsonDecode[storedPriority](b)
	return model.Priority{ID: st.ID, Name: st.Name, StatusColor: st.StatusColor, Rank: st.Rank}
}

func (s *Store) sqlExec(q string, args ...any) {
	if _, err := s.db.Exec(q, args...); err != nil {
		panic("store sqlite exec: " + err.Error())
	}
}

func (s *Store) sqlCount(q string, args ...any) int {
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		panic("store sqlite count: " + err.Error())
	}
	return n
}

func (s *Store) sqlBlob(q string, args ...any) (blob []byte, ok bool) {
	err := s.db.QueryRow(q, args...).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, false
	}
	if err != nil {
		panic("store sqlite query: " + err.Error())
	}
	return blob, true
}

func (s *Store) sqlBlobs(q string, args ...any) [][]byte {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		panic("store sqlite query: " + err.Error())
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			panic("store sqlite scan: " + err.Error())
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		panic("store sqlite rows: " + err.Error())
	}
	return out
}

func (s *Store) sqlStrings(q string, args ...any) []string {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		panic("store sqlite query: " + err.Error())
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			panic("store sqlite scan: " + err.Error())
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		panic("store sqlite rows: " + err.Error())
	}
	return out
}

// --- users ---

func (s *Store) putUserLocked(u *model.User) {
	email := ""
	if u.Email != "" {
		email = strings.ToLower(u.Email)
	}
	name := u.Name
	if name == "" {
		name = ""
	}
	s.sqlExec(`INSERT OR REPLACE INTO users(account_id, name, email, blob) VALUES(?,?,?,?)`,
		u.AccountID, name, email, jsonEncode(*u))
}

func (s *Store) userByAccountLocked(id string) *model.User {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM users WHERE account_id=?`, id)
	if !ok {
		return nil
	}
	u := jsonDecode[model.User](b)
	return &u
}

// userByKeyLocked is the old users-map key: accountId or DC username.
func (s *Store) userByKeyLocked(ref string) *model.User {
	if ref == "" {
		return nil
	}
	if u := s.userByAccountLocked(ref); u != nil {
		return u
	}
	b, ok := s.sqlBlob(`SELECT blob FROM users WHERE name=? LIMIT 1`, ref)
	if !ok {
		return nil
	}
	u := jsonDecode[model.User](b)
	return &u
}

func (s *Store) userByEmailLocked(email string) *model.User {
	if email == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM users WHERE email=?`, strings.ToLower(email))
	if !ok {
		return nil
	}
	u := jsonDecode[model.User](b)
	return &u
}

func (s *Store) usersLocked() []*model.User {
	blobs := s.sqlBlobs(`SELECT blob FROM users ORDER BY account_id`)
	out := make([]*model.User, 0, len(blobs))
	for _, b := range blobs {
		u := jsonDecode[model.User](b)
		cp := u
		out = append(out, &cp)
	}
	return out
}

func (s *Store) userCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM users`)
}

// --- projects ---

func (s *Store) putProjectLocked(p *model.Project) {
	s.sqlExec(`INSERT OR REPLACE INTO projects(key, id, blob) VALUES(?,?,?)`,
		p.Key, p.ID, jsonEncode(*p))
}

func (s *Store) projectByKeyLocked(key string) *model.Project {
	if key == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM projects WHERE key=?`, key)
	if !ok {
		return nil
	}
	p := jsonDecode[model.Project](b)
	return &p
}

func (s *Store) projectByIDLocked(id string) *model.Project {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM projects WHERE id=? LIMIT 1`, id)
	if !ok {
		return nil
	}
	p := jsonDecode[model.Project](b)
	return &p
}

func (s *Store) projectsLocked() []*model.Project {
	blobs := s.sqlBlobs(`SELECT blob FROM projects ORDER BY key`)
	out := make([]*model.Project, 0, len(blobs))
	for _, b := range blobs {
		p := jsonDecode[model.Project](b)
		cp := p
		out = append(out, &cp)
	}
	return out
}

func (s *Store) projectCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM projects`)
}

// --- statuses ---

func (s *Store) putStatusLocked(st *model.Status) {
	s.sqlExec(`INSERT OR REPLACE INTO statuses(id, blob) VALUES(?,?)`, st.ID, jsonEncode(*st))
}

func (s *Store) statusByIDLocked(id string) *model.Status {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM statuses WHERE id=?`, id)
	if !ok {
		return nil
	}
	st := jsonDecode[model.Status](b)
	return &st
}

func (s *Store) statusesLocked() []*model.Status {
	blobs := s.sqlBlobs(`SELECT blob FROM statuses ORDER BY id`)
	out := make([]*model.Status, 0, len(blobs))
	for _, b := range blobs {
		st := jsonDecode[model.Status](b)
		cp := st
		out = append(out, &cp)
	}
	return out
}

func (s *Store) statusIDsLocked() []string {
	return s.sqlStrings(`SELECT id FROM statuses ORDER BY id`)
}

func (s *Store) statusCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM statuses`)
}

// --- priorities ---

func (s *Store) putPriorityLocked(p *model.Priority) {
	s.sqlExec(`INSERT OR REPLACE INTO priorities(id, rank, blob) VALUES(?,?,?)`,
		p.ID, p.Rank, encodePriority(p))
}

func (s *Store) clearPrioritiesLocked() {
	s.sqlExec(`DELETE FROM priorities`)
}

// evictShadowedTypesLocked removes stored issue types that an incoming
// fixture is about to re-use the name of under a different id (GDK-1284).
func (s *Store) evictShadowedTypesLocked(incoming []fixtures.IssueType) {
	byName := map[string]string{}
	for _, t := range incoming {
		if n := locale.IssueTypeName(s.loc, t.ID, t.Name); n != "" {
			byName[n] = t.ID
		}
	}
	for _, t := range s.typesLocked() {
		n := locale.IssueTypeName(s.loc, t.ID, t.Name)
		if id, ok := byName[n]; ok && id != t.ID {
			s.sqlExec(`DELETE FROM issue_types WHERE id=?`, t.ID)
		}
	}
}

// evictShadowedStatusesLocked is the same rule for statuses: a transition
// named by word cannot pick between two ids.
func (s *Store) evictShadowedStatusesLocked(incoming []fixtures.Status) {
	byName := map[string]string{}
	for _, st := range incoming {
		if n := locale.StatusName(s.loc, st.ID, st.Name); n != "" {
			byName[n] = st.ID
		}
	}
	for _, st := range s.statusesLocked() {
		n := locale.StatusName(s.loc, st.ID, st.Name)
		if id, ok := byName[n]; ok && id != st.ID {
			s.sqlExec(`DELETE FROM statuses WHERE id=?`, st.ID)
		}
	}
}

func (s *Store) priorityByIDLocked(id string) *model.Priority {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM priorities WHERE id=?`, id)
	if !ok {
		return nil
	}
	p := decodePriority(b)
	return &p
}

func (s *Store) prioritiesLocked() []*model.Priority {
	blobs := s.sqlBlobs(`SELECT blob FROM priorities ORDER BY rank, id`)
	out := make([]*model.Priority, 0, len(blobs))
	for _, b := range blobs {
		p := decodePriority(b)
		cp := p
		out = append(out, &cp)
	}
	return out
}

// --- issue types ---

func (s *Store) putTypeLocked(t *model.IssueType) {
	s.sqlExec(`INSERT OR REPLACE INTO issue_types(id, blob) VALUES(?,?)`, t.ID, jsonEncode(*t))
}

func (s *Store) typeByIDLocked(id string) *model.IssueType {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM issue_types WHERE id=?`, id)
	if !ok {
		return nil
	}
	t := jsonDecode[model.IssueType](b)
	return &t
}

func (s *Store) typesLocked() []*model.IssueType {
	blobs := s.sqlBlobs(`SELECT blob FROM issue_types ORDER BY id`)
	out := make([]*model.IssueType, 0, len(blobs))
	for _, b := range blobs {
		t := jsonDecode[model.IssueType](b)
		cp := t
		out = append(out, &cp)
	}
	return out
}

// --- resolutions ---

func (s *Store) putResolutionLocked(r *model.Resolution) {
	s.sqlExec(`INSERT OR REPLACE INTO resolutions(id, blob) VALUES(?,?)`, r.ID, jsonEncode(*r))
}

func (s *Store) resolutionByIDLocked(id string) *model.Resolution {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM resolutions WHERE id=?`, id)
	if !ok {
		return nil
	}
	r := jsonDecode[model.Resolution](b)
	return &r
}

func (s *Store) resolutionsLocked() []*model.Resolution {
	blobs := s.sqlBlobs(`SELECT blob FROM resolutions ORDER BY id`)
	out := make([]*model.Resolution, 0, len(blobs))
	for _, b := range blobs {
		r := jsonDecode[model.Resolution](b)
		cp := r
		out = append(out, &cp)
	}
	return out
}

func (s *Store) resolutionIDsLocked() []string {
	return s.sqlStrings(`SELECT id FROM resolutions ORDER BY id`)
}

// --- fields ---

func (s *Store) putFieldLocked(f model.FieldInfo) {
	ord := s.sqlCount(`SELECT COUNT(*) FROM fields`)
	if b, ok := s.sqlBlob(`SELECT blob FROM fields WHERE id=?`, f.ID); ok {
		_ = b
		s.sqlExec(`UPDATE fields SET blob=? WHERE id=?`, encodeField(f), f.ID)
		return
	}
	s.sqlExec(`INSERT INTO fields(id, ord, blob) VALUES(?,?,?)`, f.ID, ord, encodeField(f))
}

func (s *Store) replaceFieldsLocked(fields []model.FieldInfo) {
	s.sqlExec(`DELETE FROM fields`)
	for i, f := range fields {
		s.sqlExec(`INSERT INTO fields(id, ord, blob) VALUES(?,?,?)`, f.ID, i, encodeField(f))
	}
}

func (s *Store) fieldsLocked() []model.FieldInfo {
	blobs := s.sqlBlobs(`SELECT blob FROM fields ORDER BY ord, id`)
	out := make([]model.FieldInfo, 0, len(blobs))
	for _, b := range blobs {
		out = append(out, decodeField(b))
	}
	return out
}

func (s *Store) fieldByIDLocked(id string) *model.FieldInfo {
	b, ok := s.sqlBlob(`SELECT blob FROM fields WHERE id=?`, id)
	if !ok {
		return nil
	}
	f := decodeField(b)
	return &f
}

// --- filters ---

func (s *Store) appendFilterLocked(f model.Filter) {
	ord := s.sqlCount(`SELECT COUNT(*) FROM filters`)
	s.sqlExec(`INSERT INTO filters(ord, blob) VALUES(?,?)`, ord, encodeFilter(f))
}

func (s *Store) filtersLocked() []model.Filter {
	blobs := s.sqlBlobs(`SELECT blob FROM filters ORDER BY ord`)
	out := make([]model.Filter, 0, len(blobs))
	for _, b := range blobs {
		out = append(out, decodeFilter(b))
	}
	return out
}

// --- transition screens ---

func (s *Store) clearTransitionScreensLocked() {
	s.sqlExec(`DELETE FROM transition_screens`)
}

func (s *Store) putTransitionScreenLocked(statusID string, fields map[string]fixtures.TransitionScreenField) {
	s.sqlExec(`INSERT OR REPLACE INTO transition_screens(status_id, blob) VALUES(?,?)`,
		statusID, jsonEncode(fields))
}

func (s *Store) transitionScreenLocked(statusID string) (map[string]fixtures.TransitionScreenField, bool) {
	b, ok := s.sqlBlob(`SELECT blob FROM transition_screens WHERE status_id=?`, statusID)
	if !ok {
		return nil, false
	}
	return jsonDecode[map[string]fixtures.TransitionScreenField](b), true
}

func (s *Store) transitionScreenIDsLocked() []string {
	return s.sqlStrings(`SELECT status_id FROM transition_screens ORDER BY status_id`)
}

// --- issues ---

func (s *Store) putIssueLocked(iss *model.Issue) {
	s.sqlExec(`INSERT OR REPLACE INTO issues(key, id, blob) VALUES(?,?,?)`,
		iss.Key, iss.ID, encodeIssue(iss))
}

func (s *Store) issueByKeyLocked(key string) *model.Issue {
	if key == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM issues WHERE key=?`, key)
	if !ok {
		return nil
	}
	iss := decodeIssue(b)
	return &iss
}

func (s *Store) issueByIDLocked(id string) *model.Issue {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM issues WHERE id=? LIMIT 1`, id)
	if !ok {
		return nil
	}
	iss := decodeIssue(b)
	return &iss
}

func (s *Store) allIssuesLocked() []*model.Issue {
	blobs := s.sqlBlobs(`SELECT blob FROM issues ORDER BY key`)
	out := make([]*model.Issue, 0, len(blobs))
	for _, b := range blobs {
		iss := decodeIssue(b)
		cp := iss
		out = append(out, &cp)
	}
	return out
}

func (s *Store) issueCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM issues`)
}

func (s *Store) issueKeysLocked() []string {
	return s.sqlStrings(`SELECT key FROM issues ORDER BY key`)
}

// --- spaces ---

func (s *Store) putSpaceLocked(sp *model.Space) {
	s.sqlExec(`INSERT OR REPLACE INTO spaces(key, id, blob) VALUES(?,?,?)`,
		sp.Key, sp.ID, jsonEncode(*sp))
}

func (s *Store) spaceByKeyLocked(key string) *model.Space {
	if key == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM spaces WHERE key=?`, key)
	if !ok {
		return nil
	}
	sp := jsonDecode[model.Space](b)
	return &sp
}

func (s *Store) spacesLocked() []*model.Space {
	blobs := s.sqlBlobs(`SELECT blob FROM spaces ORDER BY key`)
	out := make([]*model.Space, 0, len(blobs))
	for _, b := range blobs {
		sp := jsonDecode[model.Space](b)
		cp := sp
		out = append(out, &cp)
	}
	return out
}

func (s *Store) spaceCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM spaces`)
}

// --- pages ---

func (s *Store) putPageLocked(p *model.Page) {
	s.sqlExec(`INSERT OR REPLACE INTO pages(id, blob) VALUES(?,?)`, p.ID, encodePage(p))
}

func (s *Store) pageByIDLocked(id string) *model.Page {
	if id == "" {
		return nil
	}
	b, ok := s.sqlBlob(`SELECT blob FROM pages WHERE id=?`, id)
	if !ok {
		return nil
	}
	p := decodePage(b)
	return &p
}

func (s *Store) pagesLocked() []*model.Page {
	blobs := s.sqlBlobs(`SELECT blob FROM pages ORDER BY id`)
	out := make([]*model.Page, 0, len(blobs))
	for _, b := range blobs {
		p := decodePage(b)
		cp := p
		out = append(out, &cp)
	}
	return out
}

func (s *Store) pageIDsLocked() []string {
	return s.sqlStrings(`SELECT id FROM pages ORDER BY id`)
}

func (s *Store) pageCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM pages`)
}

// --- page comments ---

func (s *Store) appendPageCommentLocked(parentID string, cm model.PageComment) {
	ord := s.sqlCount(`SELECT COUNT(*) FROM page_comments WHERE parent_id=?`, parentID)
	s.sqlExec(`INSERT INTO page_comments(parent_id, ord, blob) VALUES(?,?,?)`,
		parentID, ord, encodePageComment(cm))
}

func (s *Store) pageCommentsLocked(parentID string) []model.PageComment {
	blobs := s.sqlBlobs(`SELECT blob FROM page_comments WHERE parent_id=? ORDER BY ord`, parentID)
	out := make([]model.PageComment, 0, len(blobs))
	for _, b := range blobs {
		out = append(out, decodePageComment(b))
	}
	return out
}

func (s *Store) pageCommentParentIDsLocked() []string {
	return s.sqlStrings(`SELECT DISTINCT parent_id FROM page_comments ORDER BY parent_id`)
}

func (s *Store) pageCommentCountLocked() int {
	return s.sqlCount(`SELECT COUNT(*) FROM page_comments`)
}

// --- attachment bytes ---

func (s *Store) putAttachBytesLocked(id string, body []byte) {
	if body == nil {
		body = []byte{}
	}
	s.sqlExec(`INSERT OR REPLACE INTO attachments(id, bytes) VALUES(?,?)`, id, body)
}

func (s *Store) attachBytesLocked(id string) ([]byte, bool) {
	b, ok := s.sqlBlob(`SELECT bytes FROM attachments WHERE id=?`, id)
	if !ok {
		return nil, false
	}
	return b, true
}

// nextSeqLocked mints the next value of a named id counter in the working
// copy itself (a store_meta "seq:<name>" row), not in process memory: the
// persist is one working copy shared by every process that opened it, and
// per-process counters seeded once at Open handed the same id to different
// issues (gadak GDK-1180). The UPSERT..RETURNING is a single statement,
// atomic under SQLite's write lock, on-disk and :memory: alike.
func (s *Store) nextSeqLocked(name string) int {
	var v int
	if err := s.db.QueryRow(`INSERT INTO store_meta(k, v) VALUES('seq:'||?1, '1')
ON CONFLICT(k) DO UPDATE SET v = CAST(CAST(v AS INTEGER)+1 AS TEXT)
RETURNING CAST(v AS INTEGER)`, name).Scan(&v); err != nil {
		panic("store sqlite seq: " + err.Error())
	}
	return v
}

// floorSeqLocked raises the named counter to at least v — the Open-time
// seed from ids already present in the data. It never lowers a value a
// concurrent process may have advanced further.
func (s *Store) floorSeqLocked(name string, v int) {
	s.sqlExec(`INSERT INTO store_meta(k, v) VALUES('seq:'||?1, CAST(?2 AS TEXT))
ON CONFLICT(k) DO UPDATE SET v = CAST(MAX(CAST(v AS INTEGER), ?2) AS TEXT)`, name, v)
}
