// Package locale overlays localized display names onto stable ids.
//
// A live ko_KR Cloud site (GET /rest/api/3/status, /issuetype, /priority,
// /field on 2026-08-15) showed:
//
//   - status and issue-type names localized (진행 중, 작업, 버그, …)
//   - statusCategory.name localized; statusCategory.key stable
//   - priority names still English (Highest/High/Medium/Low/Lowest)
//   - field catalog names localized (이슈 유형, 담당자, 우선 순위)
//   - two statuses can share a display name with different ids
//   - two issue types can share a display name with different ids
//
// issuetap still localizes priorities under --locale ko so a client that
// keys on "High" fails here even when a particular real site would not.
// That is the product: make the trap fail loudly. docs/LOCALES.md records
// the observed-vs-served distinction.
package locale

import (
	"strings"

	"github.com/midagedev/issuetap/internal/model"
)

// Code is a supported locale.
type Code string

const (
	EN Code = "en"
	KO Code = "ko"
	JA Code = "ja"
	DE Code = "de"
)

// Parse accepts en, ko, ja, de and the BCP-47 forms (en_US, ko_KR, ja_JP, de_DE).
func Parse(s string) Code {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "" || strings.HasPrefix(s, "en"):
		return EN
	case strings.HasPrefix(s, "ko"):
		return KO
	case strings.HasPrefix(s, "ja"):
		return JA
	case strings.HasPrefix(s, "de"):
		return DE
	default:
		return EN
	}
}

// BCP47 is the locale string /myself returns.
func BCP47(c Code) string {
	switch c {
	case KO:
		return "ko_KR"
	case JA:
		return "ja_JP"
	case DE:
		return "de_DE"
	default:
		return "en_US"
	}
}

type names struct {
	status     map[string]string // id → name
	statusByEN map[string]string // english name → name
	category   map[string]string // key → name
	priority   map[string]string
	issueType  map[string]string
	typeByEN   map[string]string
	field      map[string]string
	resolution map[string]string
	changelog  map[string]string // fieldId → display Field
}

var catalogs = map[Code]names{
	EN: {
		status: map[string]string{
			"1": "To Do", "3": "In Progress", "5": "Done",
			"10000": "To Do", "10001": "In Progress", "10002": "In Review",
			"10003": "Done", "10016": "Backlog", "10017": "Selected for Development",
			"10018": "Done",
		},
		statusByEN: map[string]string{
			"to do": "To Do", "in progress": "In Progress", "done": "Done",
			"in review": "In Review", "backlog": "Backlog",
			"selected for development": "Selected for Development",
		},
		category: map[string]string{
			"new": "To Do", "indeterminate": "In Progress", "inprogress": "In Progress", "done": "Done",
		},
		priority: map[string]string{
			"1": "Highest", "2": "High", "3": "Medium", "4": "Low", "5": "Lowest",
		},
		issueType: map[string]string{
			"10000": "Epic", "10001": "Epic", "10002": "Sub-task", "10003": "Task",
			"10004": "Story", "10005": "Feature", "10006": "Request", "10007": "Bug",
			"10026": "Story", "10027": "Task", "10028": "Sub-task", "10029": "Bug",
		},
		typeByEN: map[string]string{
			"epic": "Epic", "task": "Task", "bug": "Bug", "story": "Story",
			"sub-task": "Sub-task", "subtask": "Sub-task", "feature": "Feature", "request": "Request",
		},
		field:      enFields,
		resolution: map[string]string{"10000": "Done", "10001": "Won't Do", "10002": "Duplicate", "10003": "Cannot Reproduce"},
		changelog:  enChangelog,
	},
	KO: {
		status: map[string]string{
			"1": "할 일", "3": "진행 중", "5": "완료",
			"10000": "해야 할 일", "10001": "진행 중", "10002": "검토 중",
			"10003": "완료", "10016": "Backlog", "10017": "Selected for Development",
			"10018": "완료",
		},
		statusByEN: map[string]string{
			"to do": "해야 할 일", "in progress": "진행 중", "done": "완료",
			"in review": "검토 중", "backlog": "Backlog",
			"selected for development": "Selected for Development",
			"할 일": "해야 할 일",
		},
		category: map[string]string{
			"new": "해야 할 일", "indeterminate": "진행 중", "inprogress": "진행 중", "done": "완료",
		},
		priority: map[string]string{
			"1": "가장 높음", "2": "높음", "3": "중간", "4": "낮음", "5": "가장 낮음",
		},
		issueType: map[string]string{
			"10000": "에픽", "10001": "에픽", "10002": "하위 작업", "10003": "작업",
			"10004": "스토리", "10005": "기능", "10006": "요청", "10007": "버그",
			"10026": "스토리", "10027": "작업", "10028": "하위 작업", "10029": "버그",
		},
		typeByEN: map[string]string{
			"epic": "에픽", "task": "작업", "bug": "버그", "story": "스토리",
			"sub-task": "하위 작업", "subtask": "하위 작업", "feature": "기능", "request": "요청",
		},
		field:      koFields,
		resolution: map[string]string{"10000": "완료", "10001": "Won't Do", "10002": "복제", "10003": "재현 불가"},
		changelog:  koChangelog,
	},
	JA: {
		status: map[string]string{
			"1": "作業前", "3": "進行中", "5": "完了",
			"10000": "作業前", "10001": "進行中", "10002": "レビュー中",
			"10003": "完了", "10016": "Backlog", "10017": "Selected for Development",
			"10018": "完了",
		},
		statusByEN: map[string]string{
			"to do": "作業前", "in progress": "進行中", "done": "完了",
			"in review": "レビュー中", "backlog": "Backlog",
			"selected for development": "Selected for Development",
		},
		category: map[string]string{
			"new": "作業前", "indeterminate": "進行中", "inprogress": "進行中", "done": "完了",
		},
		priority: map[string]string{
			"1": "最高", "2": "高", "3": "中", "4": "低", "5": "最低",
		},
		issueType: map[string]string{
			"10000": "エピック", "10001": "エピック", "10002": "サブタスク", "10003": "タスク",
			"10004": "ストーリー", "10005": "機能", "10006": "リクエスト", "10007": "バグ",
			"10026": "ストーリー", "10027": "タスク", "10028": "サブタスク", "10029": "バグ",
		},
		typeByEN: map[string]string{
			"epic": "エピック", "task": "タスク", "bug": "バグ", "story": "ストーリー",
			"sub-task": "サブタスク", "subtask": "サブタスク", "feature": "機能", "request": "リクエスト",
		},
		field:      jaFields,
		resolution: map[string]string{"10000": "完了", "10001": "対応しない", "10002": "重複", "10003": "再現不可"},
		changelog:  jaChangelog,
	},
	DE: {
		status: map[string]string{
			"1": "Zu erledigen", "3": "In Arbeit", "5": "Fertig",
			"10000": "Zu erledigen", "10001": "In Arbeit", "10002": "In Prüfung",
			"10003": "Fertig", "10016": "Backlog", "10017": "Selected for Development",
			"10018": "Fertig",
		},
		statusByEN: map[string]string{
			"to do": "Zu erledigen", "in progress": "In Arbeit", "done": "Fertig",
			"in review": "In Prüfung", "backlog": "Backlog",
			"selected for development": "Selected for Development",
		},
		category: map[string]string{
			"new": "Zu erledigen", "indeterminate": "In Arbeit", "inprogress": "In Arbeit", "done": "Fertig",
		},
		priority: map[string]string{
			"1": "Höchste", "2": "Hoch", "3": "Mittel", "4": "Niedrig", "5": "Niedrigste",
		},
		issueType: map[string]string{
			"10000": "Epic", "10001": "Epic", "10002": "Unteraufgabe", "10003": "Aufgabe",
			"10004": "Story", "10005": "Feature", "10006": "Anfrage", "10007": "Fehler",
			"10026": "Story", "10027": "Aufgabe", "10028": "Unteraufgabe", "10029": "Fehler",
		},
		typeByEN: map[string]string{
			"epic": "Epic", "task": "Aufgabe", "bug": "Fehler", "story": "Story",
			"sub-task": "Unteraufgabe", "subtask": "Unteraufgabe", "feature": "Feature", "request": "Anfrage",
		},
		field:      deFields,
		resolution: map[string]string{"10000": "Erledigt", "10001": "Wird nicht erledigt", "10002": "Duplikat", "10003": "Nicht reproduzierbar"},
		changelog:  deChangelog,
	},
}

// English field names are the Jira system defaults.
var enFields = map[string]string{
	"issuetype": "Issue Type", "priority": "Priority", "status": "Status",
	"assignee": "Assignee", "reporter": "Reporter", "summary": "Summary",
	"description": "Description", "comment": "Comment", "labels": "Labels",
	"components": "Components", "fixVersions": "Fix Version/s", "created": "Created",
	"updated": "Updated", "resolution": "Resolution", "environment": "Environment",
	"statusCategory": "Status Category", "project": "Project", "parent": "Parent",
}

// koFields matches GET /rest/api/3/field on a ko_KR site (2026-08-15).
var koFields = map[string]string{
	"issuetype": "이슈 유형", "priority": "우선 순위", "status": "상태",
	"assignee": "담당자", "reporter": "보고자", "summary": "요약",
	"description": "설명", "comment": "댓글", "labels": "레이블",
	"components": "컴포넌트", "fixVersions": "수정 버전", "created": "만듦",
	"updated": "업데이트", "resolution": "해결", "environment": "환경",
	"statusCategory": "상태 범주", "project": "프로젝트", "parent": "상위 항목",
}

var jaFields = map[string]string{
	"issuetype": "課題タイプ", "priority": "優先度", "status": "ステータス",
	"assignee": "担当者", "reporter": "報告者", "summary": "要約",
	"description": "説明", "comment": "コメント", "labels": "ラベル",
	"components": "コンポーネント", "fixVersions": "修正バージョン", "created": "作成日",
	"updated": "更新日", "resolution": "解決状況", "environment": "環境",
	"statusCategory": "ステータス カテゴリ", "project": "プロジェクト", "parent": "親",
}

var deFields = map[string]string{
	"issuetype": "Vorgangstyp", "priority": "Priorität", "status": "Status",
	"assignee": "Bearbeiter", "reporter": "Autor", "summary": "Zusammenfassung",
	"description": "Beschreibung", "comment": "Kommentar", "labels": "Stichwörter",
	"components": "Komponenten", "fixVersions": "Lösungsversion", "created": "Erstellt",
	"updated": "Aktualisiert", "resolution": "Lösung", "environment": "Umgebung",
	"statusCategory": "Statuskategorie", "project": "Projekt", "parent": "Vorgänger",
}

var enChangelog = map[string]string{
	"status": "status", "assignee": "assignee", "reporter": "reporter",
	"priority": "priority", "summary": "summary", "description": "description",
	"issuetype": "Issue Type", "resolution": "resolution", "labels": "labels",
	"components": "Component", "fixVersions": "Fix Version", "duedate": "Due Date",
	"environment": "environment",
}

var koChangelog = map[string]string{
	"status": "상태", "assignee": "담당자", "reporter": "보고자",
	"priority": "우선 순위", "summary": "요약", "description": "설명",
	"issuetype": "이슈 유형", "resolution": "해결", "labels": "레이블",
	"components": "구성 요소", "fixVersions": "수정 버전", "duedate": "기한",
	"environment": "환경",
}

var jaChangelog = map[string]string{
	"status": "ステータス", "assignee": "担当者", "reporter": "報告者",
	"priority": "優先度", "summary": "要約", "description": "説明",
	"issuetype": "課題タイプ", "resolution": "解決状況", "labels": "ラベル",
	"components": "コンポーネント", "fixVersions": "修正バージョン", "duedate": "期限",
	"environment": "環境",
}

var deChangelog = map[string]string{
	"status": "Status", "assignee": "Bearbeiter", "reporter": "Autor",
	"priority": "Priorität", "summary": "Zusammenfassung", "description": "Beschreibung",
	"issuetype": "Vorgangstyp", "resolution": "Lösung", "labels": "Stichwörter",
	"components": "Komponente", "fixVersions": "Lösungsversion", "duedate": "Fälligkeitsdatum",
	"environment": "Umgebung",
}

func cat(c Code) names {
	n, ok := catalogs[c]
	if !ok {
		return catalogs[EN]
	}
	return n
}

// StatusName returns the localized name for a status id, falling back to
// stored / English name overlay, then the stored name itself.
func StatusName(c Code, id, stored string) string {
	n := cat(c)
	if id != "" {
		if v, ok := n.status[id]; ok {
			return v
		}
	}
	if stored != "" {
		if v, ok := n.statusByEN[strings.ToLower(stored)]; ok {
			return v
		}
	}
	if stored != "" {
		return stored
	}
	return id
}

// CategoryName localizes statusCategory.name. Key stays whatever the caller has.
func CategoryName(c Code, key string) string {
	if v, ok := cat(c).category[key]; ok {
		return v
	}
	return key
}

// PriorityName localizes a priority. Unknown ids keep the stored name.
func PriorityName(c Code, id, stored string) string {
	if v, ok := cat(c).priority[id]; ok {
		return v
	}
	// English stored name → locale via rank-less lookup.
	switch strings.ToLower(stored) {
	case "highest":
		return cat(c).priority["1"]
	case "high":
		return cat(c).priority["2"]
	case "medium":
		return cat(c).priority["3"]
	case "low":
		return cat(c).priority["4"]
	case "lowest":
		return cat(c).priority["5"]
	}
	if stored != "" {
		return stored
	}
	return id
}

// IssueTypeName localizes an issue type.
func IssueTypeName(c Code, id, stored string) string {
	n := cat(c)
	if id != "" {
		if v, ok := n.issueType[id]; ok {
			return v
		}
	}
	if stored != "" {
		if v, ok := n.typeByEN[strings.ToLower(stored)]; ok {
			return v
		}
	}
	if stored != "" {
		return stored
	}
	return id
}

// FieldName localizes a system field id (issuetype, status, …).
func FieldName(c Code, id, stored string) string {
	if v, ok := cat(c).field[id]; ok {
		return v
	}
	if stored != "" {
		return stored
	}
	return id
}

// ResolutionName localizes a resolution.
func ResolutionName(c Code, id, stored string) string {
	if v, ok := cat(c).resolution[id]; ok {
		return v
	}
	if stored != "" {
		return stored
	}
	return id
}

// ChangelogField is the localized HistoryItem.Field for a stable fieldId.
func ChangelogField(c Code, fieldID, stored string) string {
	if v, ok := cat(c).changelog[fieldID]; ok {
		return v
	}
	if stored != "" {
		return stored
	}
	return fieldID
}

// OverlayStatus returns a copy of s with Name and Category.Name localized.
func OverlayStatus(c Code, s model.Status) model.Status {
	out := s
	out.Name = StatusName(c, s.ID, s.Name)
	if s.Untranslated == "" {
		out.Untranslated = s.Name
	}
	out.StatusCategory.Name = CategoryName(c, s.StatusCategory.Key)
	if out.StatusCategory.ID == 0 {
		out.StatusCategory.ID = model.CategoryID(s.StatusCategory.Key)
	}
	if out.StatusCategory.ColorName == "" {
		out.StatusCategory.ColorName = model.CategoryColor(s.StatusCategory.Key)
	}
	return out
}

// OverlayPriority localizes a priority.
func OverlayPriority(c Code, p model.Priority) model.Priority {
	out := p
	out.Name = PriorityName(c, p.ID, p.Name)
	return out
}

// OverlayIssueType localizes an issue type.
func OverlayIssueType(c Code, t model.IssueType) model.IssueType {
	out := t
	out.Name = IssueTypeName(c, t.ID, t.Name)
	if out.Untranslated == "" {
		out.Untranslated = t.Name
	}
	return out
}

// OverlayField localizes a field catalog row.
func OverlayField(c Code, f model.FieldInfo) model.FieldInfo {
	out := f
	out.Name = FieldName(c, f.ID, f.Name)
	return out
}

// OverlayResolution localizes a resolution.
func OverlayResolution(c Code, r model.Resolution) model.Resolution {
	out := r
	out.Name = ResolutionName(c, r.ID, r.Name)
	return out
}
