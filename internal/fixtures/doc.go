// Package fixtures loads and snapshots the YAML/JSON document that seeds
// issuetap. YAML is the authored format (scenarios sit next to fixtures and
// both siblings write scenarios in YAML); JSON is accepted so a snapshot
// round-trip and a generated document both load.
package fixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is the on-disk fixture.
type Doc struct {
	Seed        int64        `json:"seed,omitempty" yaml:"seed,omitempty"`
	Locale      string       `json:"locale,omitempty" yaml:"locale,omitempty"`
	Timezone    string       `json:"timezone,omitempty" yaml:"timezone,omitempty"`
	Users       []User       `json:"users,omitempty" yaml:"users,omitempty"`
	Projects    []Project    `json:"projects,omitempty" yaml:"projects,omitempty"`
	Statuses    []Status     `json:"statuses,omitempty" yaml:"statuses,omitempty"`
	Priorities  []Priority   `json:"priorities,omitempty" yaml:"priorities,omitempty"`
	IssueTypes  []IssueType  `json:"issueTypes,omitempty" yaml:"issueTypes,omitempty"`
	Resolutions []Resolution `json:"resolutions,omitempty" yaml:"resolutions,omitempty"`
	Fields      []Field      `json:"fields,omitempty" yaml:"fields,omitempty"`
	Filters     []Filter     `json:"filters,omitempty" yaml:"filters,omitempty"`
	Issues      []Issue      `json:"issues,omitempty" yaml:"issues,omitempty"`
	Spaces      []Space      `json:"spaces,omitempty" yaml:"spaces,omitempty"`
	Pages       []Page       `json:"pages,omitempty" yaml:"pages,omitempty"`
}

type User struct {
	AccountID   string `json:"accountId,omitempty" yaml:"accountId,omitempty"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Key         string `json:"key,omitempty" yaml:"key,omitempty"`
	DisplayName string `json:"displayName" yaml:"displayName"`
	Email       string `json:"email,omitempty" yaml:"email,omitempty"`
	Active      *bool  `json:"active,omitempty" yaml:"active,omitempty"`
	TimeZone    string `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`
}

type Project struct {
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Key     string `json:"key" yaml:"key"`
	Name    string `json:"name" yaml:"name"`
	TypeKey string `json:"type,omitempty" yaml:"type,omitempty"`
	Style   string `json:"style,omitempty" yaml:"style,omitempty"`
}

type Status struct {
	ID       string `json:"id" yaml:"id"`
	Name     string `json:"name" yaml:"name"`
	Category string `json:"category" yaml:"category"` // new | indeterminate | done
}

type Priority struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

type IssueType struct {
	ID             string `json:"id" yaml:"id"`
	Name           string `json:"name" yaml:"name"`
	HierarchyLevel int    `json:"hierarchyLevel,omitempty" yaml:"hierarchyLevel,omitempty"`
	Subtask        bool   `json:"subtask,omitempty" yaml:"subtask,omitempty"`
}

type Resolution struct {
	ID   string `json:"id" yaml:"id"`
	Name string `json:"name" yaml:"name"`
}

type Field struct {
	ID     string `json:"id" yaml:"id"`
	Name   string `json:"name" yaml:"name"`
	Custom bool   `json:"custom,omitempty" yaml:"custom,omitempty"`
	Type   string `json:"type,omitempty" yaml:"type,omitempty"`
}

type Filter struct {
	ID        string `json:"id" yaml:"id"`
	Name      string `json:"name" yaml:"name"`
	JQL       string `json:"jql" yaml:"jql"`
	Favourite bool   `json:"favourite,omitempty" yaml:"favourite,omitempty"`
	Owner     string `json:"owner,omitempty" yaml:"owner,omitempty"`
}

type Issue struct {
	ID          string         `json:"id,omitempty" yaml:"id,omitempty"`
	Key         string         `json:"key" yaml:"key"`
	Summary     string         `json:"summary" yaml:"summary"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Environment string         `json:"environment,omitempty" yaml:"environment,omitempty"`
	Type        string         `json:"type,omitempty" yaml:"type,omitempty"`     // id or name
	Status      string         `json:"status,omitempty" yaml:"status,omitempty"` // id or name
	Priority    string         `json:"priority,omitempty" yaml:"priority,omitempty"`
	Assignee    string         `json:"assignee,omitempty" yaml:"assignee,omitempty"`
	Reporter    string         `json:"reporter,omitempty" yaml:"reporter,omitempty"`
	Creator     string         `json:"creator,omitempty" yaml:"creator,omitempty"`
	Project     string         `json:"project,omitempty" yaml:"project,omitempty"`
	Parent      string         `json:"parent,omitempty" yaml:"parent,omitempty"`
	Labels      []string       `json:"labels,omitempty" yaml:"labels,omitempty"`
	Components  []string       `json:"components,omitempty" yaml:"components,omitempty"`
	FixVersions []string       `json:"fixVersions,omitempty" yaml:"fixVersions,omitempty"`
	Versions    []string       `json:"versions,omitempty" yaml:"versions,omitempty"`
	Duedate     string         `json:"duedate,omitempty" yaml:"duedate,omitempty"`
	Resolution  string         `json:"resolution,omitempty" yaml:"resolution,omitempty"`
	Created     string         `json:"created,omitempty" yaml:"created,omitempty"`
	Updated     string         `json:"updated,omitempty" yaml:"updated,omitempty"`
	Comments    []Comment      `json:"comments,omitempty" yaml:"comments,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty" yaml:"attachments,omitempty"`
	Links       []Link         `json:"links,omitempty" yaml:"links,omitempty"`
	History     []History      `json:"history,omitempty" yaml:"history,omitempty"`
	Custom      map[string]any `json:"custom,omitempty" yaml:"custom,omitempty"`
}

type Comment struct {
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Author  string `json:"author,omitempty" yaml:"author,omitempty"`
	Body    string `json:"body" yaml:"body"`
	Created string `json:"created,omitempty" yaml:"created,omitempty"`
	Updated string `json:"updated,omitempty" yaml:"updated,omitempty"`
}

type Attachment struct {
	ID         string `json:"id,omitempty" yaml:"id,omitempty"`
	Filename   string `json:"filename" yaml:"filename"`
	MimeType   string `json:"mimeType,omitempty" yaml:"mimeType,omitempty"`
	Text       string `json:"text,omitempty" yaml:"text,omitempty"`             // readable inline content (printable UTF-8)
	DataBase64 string `json:"dataBase64,omitempty" yaml:"dataBase64,omitempty"` // binary content, std base64
	Author     string `json:"author,omitempty" yaml:"author,omitempty"`
	Created    string `json:"created,omitempty" yaml:"created,omitempty"`
}

type Link struct {
	Type    string `json:"type" yaml:"type"`
	Inward  string `json:"inward,omitempty" yaml:"inward,omitempty"`
	Outward string `json:"outward,omitempty" yaml:"outward,omitempty"`
}

type History struct {
	ID     string        `json:"id,omitempty" yaml:"id,omitempty"`
	At     string        `json:"at" yaml:"at"`
	Author string        `json:"author,omitempty" yaml:"author,omitempty"`
	Items  []HistoryItem `json:"items" yaml:"items"`
}

type HistoryItem struct {
	Field      string `json:"field" yaml:"field"`
	FieldID    string `json:"fieldId,omitempty" yaml:"fieldId,omitempty"`
	From       string `json:"from,omitempty" yaml:"from,omitempty"`
	FromString string `json:"fromString,omitempty" yaml:"fromString,omitempty"`
	To         string `json:"to,omitempty" yaml:"to,omitempty"`
	ToString   string `json:"toString,omitempty" yaml:"toString,omitempty"`
}

type Space struct {
	ID       string `json:"id,omitempty" yaml:"id,omitempty"`
	Key      string `json:"key" yaml:"key"`
	Name     string `json:"name" yaml:"name"`
	Type     string `json:"type,omitempty" yaml:"type,omitempty"`
	Homepage string `json:"homepage,omitempty" yaml:"homepage,omitempty"`
}

type Page struct {
	ID       string        `json:"id,omitempty" yaml:"id,omitempty"`
	Type     string        `json:"type,omitempty" yaml:"type,omitempty"`
	Status   string        `json:"status,omitempty" yaml:"status,omitempty"`
	Title    string        `json:"title" yaml:"title"`
	Space    string        `json:"space" yaml:"space"`
	Version  int           `json:"version,omitempty" yaml:"version,omitempty"`
	When     string        `json:"when,omitempty" yaml:"when,omitempty"`
	Author   string        `json:"author,omitempty" yaml:"author,omitempty"`
	Body     string        `json:"body,omitempty" yaml:"body,omitempty"`
	Labels   []string      `json:"labels,omitempty" yaml:"labels,omitempty"`
	Parent   string        `json:"parent,omitempty" yaml:"parent,omitempty"`
	Comments []PageComment `json:"comments,omitempty" yaml:"comments,omitempty"`
	// Versions is the page's Confluence version history (message included).
	// Omitted on an authored fixture: Apply synthesizes one row from
	// version/when/author so GET /version still has a current stamp.
	Versions []PageVersion `json:"versions,omitempty" yaml:"versions,omitempty"`
}

// PageVersion is one historical version of a wiki page. message is the
// editor's "what changed" note and must survive snapshot/persist.
type PageVersion struct {
	Number    int    `json:"number" yaml:"number"`
	When      string `json:"when,omitempty" yaml:"when,omitempty"`
	Author    string `json:"author,omitempty" yaml:"author,omitempty"`
	Message   string `json:"message,omitempty" yaml:"message,omitempty"`
	MinorEdit bool   `json:"minorEdit,omitempty" yaml:"minorEdit,omitempty"`
}

type PageComment struct {
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Author  string `json:"author,omitempty" yaml:"author,omitempty"`
	Body    string `json:"body" yaml:"body"`
	When    string `json:"when,omitempty" yaml:"when,omitempty"`
	ReplyTo string `json:"replyTo,omitempty" yaml:"replyTo,omitempty"`
}

// Load reads a YAML or JSON fixture from disk.
func Load(path string) (Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Doc{}, err
	}
	return Parse(b, filepath.Ext(path))
}

// Parse decodes a fixture. ext selects YAML vs JSON; empty ext sniffs.
func Parse(b []byte, ext string) (Doc, error) {
	b = bytes.TrimSpace(b)
	var d Doc
	useJSON := strings.EqualFold(ext, ".json") || (ext == "" && len(b) > 0 && b[0] == '{')
	var err error
	if useJSON {
		err = json.Unmarshal(b, &d)
	} else {
		err = yaml.Unmarshal(b, &d)
	}
	if err != nil {
		return Doc{}, fmt.Errorf("fixture: %w", err)
	}
	if err := validate(d); err != nil {
		return Doc{}, err
	}
	return d, nil
}

func validate(d Doc) error {
	keys := map[string]bool{}
	for i, iss := range d.Issues {
		if iss.Key == "" {
			return fmt.Errorf("fixture: issues[%d] missing key", i)
		}
		if keys[iss.Key] {
			return fmt.Errorf("fixture: duplicate issue key %s", iss.Key)
		}
		keys[iss.Key] = true
	}
	pkeys := map[string]bool{}
	for i, p := range d.Projects {
		if p.Key == "" {
			return fmt.Errorf("fixture: projects[%d] missing key", i)
		}
		if pkeys[p.Key] {
			return fmt.Errorf("fixture: duplicate project key %s", p.Key)
		}
		pkeys[p.Key] = true
	}
	skeys := map[string]bool{}
	for i, s := range d.Spaces {
		if s.Key == "" {
			return fmt.Errorf("fixture: spaces[%d] missing key", i)
		}
		if skeys[s.Key] {
			return fmt.Errorf("fixture: duplicate space key %s", s.Key)
		}
		skeys[s.Key] = true
	}
	return nil
}

// MarshalYAML is the snapshot form.
func MarshalYAML(d Doc) ([]byte, error) {
	return yaml.Marshal(d)
}

// MarshalJSON is the snapshot form.
func MarshalJSON(d Doc) ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}
