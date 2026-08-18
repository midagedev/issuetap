// Package model is the dialect-neutral in-memory graph issuetap serves.
//
// Display names (status, priority, issue type, field labels, changelog
// Field) are localized at serve time. Logic must key on ids and
// statusCategory.key — never on a name. That is the trap this product exists
// to make fail loudly; see docs/LOCALES.md.
package model

import "encoding/json"

// JiraTime is the Cloud timestamp layout. time.RFC3339 does not parse it
// (no colon in the offset). gadak.internal/jira.Layout is the same string;
// we do not import gadak.
const JiraTime = "2006-01-02T15:04:05.000-0700"

// Named is a {id,name} pair used throughout Jira payloads.
type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User is an Atlassian account. Cloud uses AccountID; Data Center fills
// Name/Key (username/userKey) and may leave AccountID empty.
type User struct {
	AccountID   string            `json:"accountId,omitempty"`
	Name        string            `json:"name,omitempty"` // DC username
	Key         string            `json:"key,omitempty"`  // DC userKey
	DisplayName string            `json:"displayName"`
	Email       string            `json:"emailAddress,omitempty"`
	Active      bool              `json:"active"`
	TimeZone    string            `json:"timeZone,omitempty"`
	Locale      string            `json:"locale,omitempty"`
	AvatarURLs  map[string]string `json:"avatarUrls,omitempty"`
	AccountType string            `json:"accountType,omitempty"`
}

// Status is a workflow status. Category.Key is the only stable axis
// (new | indeterminate | done). Name is localized.
type Status struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Untranslated   string         `json:"untranslatedName,omitempty"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// StatusCategory is Jira's three-bucket classifier. Key is stable across
// locales; Name is not (observed on a ko_KR Cloud site: 해야 할 일 / 진행 중 / 완료).
type StatusCategory struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	ColorName string `json:"colorName,omitempty"`
}

// Priority is ordered most-urgent-first. Rank is 0 for the first entry.
// Observed: a ko_KR Cloud site left priority names in English.
type Priority struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	StatusColor string `json:"statusColor,omitempty"`
	Rank        int    `json:"-"`
}

// IssueType carries hierarchyLevel so clients can tell epic / standard / sub-task
// without reading the (localized) name.
type IssueType struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Untranslated   string `json:"untranslatedName,omitempty"`
	HierarchyLevel int    `json:"hierarchyLevel"`
	Subtask        bool   `json:"subtask"`
	Description    string `json:"description,omitempty"`
}

// Resolution is a done-reason. Names localize.
type Resolution struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Project is one Jira project.
type Project struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	TypeKey    string `json:"projectTypeKey"`
	Style      string `json:"style,omitempty"`
	Simplified bool   `json:"simplified,omitempty"`
}

// Comment is an issue comment. Body is ADF (Cloud) or a wiki-markup string (DC)
// depending on the dialect serializer — the store keeps ADF plus a plain text.
type Comment struct {
	ID       string          `json:"id"`
	Author   User            `json:"author"`
	Body     json.RawMessage `json:"body"`
	BodyText string          `json:"-"`
	Created  string          `json:"created"`
	Updated  string          `json:"updated"`
}

// Attachment metadata. Bytes live on the store keyed by ID.
type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	Author   User   `json:"author"`
	Created  string `json:"created"`
	MediaID  string `json:"-"`
}

// IssueLink is one inward or outward link.
type IssueLink struct {
	TypeName   string `json:"-"`
	InwardKey  string `json:"-"`
	OutwardKey string `json:"-"`
}

// HistoryItem is one field change. Field is the localized display name;
// FieldID is the stable identifier.
type HistoryItem struct {
	Field      string `json:"field"`
	FieldID    string `json:"fieldId"`
	From       string `json:"from"`
	FromString string `json:"fromString"`
	To         string `json:"to"`
	ToString   string `json:"toString"`
}

// History is one changelog group (one user action, one or more items).
type History struct {
	ID      string        `json:"id"`
	Created string        `json:"created"`
	Author  User          `json:"author"`
	Items   []HistoryItem `json:"items"`
}

// FieldInfo is one row of GET /field.
type FieldInfo struct {
	ID         string      `json:"id"`
	Key        string      `json:"key,omitempty"`
	Name       string      `json:"name"`
	Custom     bool        `json:"custom"`
	Clause     []string    `json:"clauseNames,omitempty"`
	Schema     FieldSchema `json:"schema"`
	Orderable  bool        `json:"orderable"`
	Navigable  bool        `json:"navigable"`
	Searchable bool        `json:"searchable"`
}

// FieldSchema is the Jira field schema fragment.
type FieldSchema struct {
	Type   string `json:"type,omitempty"`
	Items  string `json:"items,omitempty"`
	Custom string `json:"custom,omitempty"`
	System string `json:"system,omitempty"`
}

// Filter is a saved JQL filter (GET /filter/my).
type Filter struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	JQL       string `json:"jql"`
	Favourite bool   `json:"favourite"`
	Owner     string `json:"-"`
}

// Transition is one available status change.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	ToID string `json:"-"`
}

// Issue is the store record. Serializer applies locale + dialect.
type Issue struct {
	ID              string
	Key             string
	Summary         string
	DescriptionADF  json.RawMessage
	DescriptionText string
	EnvironmentADF  json.RawMessage
	EnvironmentText string
	IssueTypeID     string
	StatusID        string
	PriorityID      string
	AssigneeID      string
	ReporterID      string
	CreatorID       string
	ProjectKey      string
	ParentKey       string
	Labels          []string
	Components      []Named
	FixVersions     []Named
	Versions        []Named
	Duedate         string
	ResolutionID    string
	Created         string
	Updated         string
	Comments        []Comment
	Attachments     []Attachment
	Links           []IssueLink
	Histories       []History
	Custom          map[string]any
}

// Space is a Confluence space.
type Space struct {
	ID         string
	Key        string
	Name       string
	Type       string // global | personal
	Status     string
	HomepageID string
}

// PageVersion is one Confluence content version. Number is 1-based.
// Message is the editor's "what changed" note and must round-trip.
type PageVersion struct {
	Number    int
	When      string
	AuthorID  string
	Message   string
	MinorEdit bool
}

// Page is a Confluence content row.
type Page struct {
	ID          string
	Type        string // page | comment | blogpost
	Status      string
	Title       string
	SpaceKey    string
	Version     int
	When        string
	AuthorID    string
	BodyADF     json.RawMessage
	BodyText    string
	BodyStorage string // XHTML for DC
	Labels      []string
	Ancestors   []string // parent chain, last is direct parent
	Container   string   // for comments: page id
	WebUI       string
	Versions    []PageVersion // ascending number; GET /version serves newest-first
}

// PageComment is a child comment (or reply) on a page.
type PageComment struct {
	ID       string
	Title    string
	ParentID string // page id or parent comment id
	BodyADF  json.RawMessage
	BodyText string
	Version  int
	When     string
	AuthorID string
}

// Category maps a statusCategory.key onto gadak's three stored values.
// Unknown keys become "new" so a miss can only lose a reopen, never invent one.
func Category(key string) string {
	switch key {
	case "done":
		return "done"
	case "indeterminate", "inprogress":
		return "inprogress"
	default:
		return "new"
	}
}

// CategoryID is the Cloud numeric id observed on a live site (2026-08-15):
// new=2, done=3, indeterminate=4.
func CategoryID(key string) int {
	switch key {
	case "done":
		return 3
	case "indeterminate", "inprogress":
		return 4
	default:
		return 2
	}
}

// CategoryColor is Cloud's colour name for the three buckets.
func CategoryColor(key string) string {
	switch key {
	case "done":
		return "green"
	case "indeterminate", "inprogress":
		return "yellow"
	default:
		return "blue-gray"
	}
}
