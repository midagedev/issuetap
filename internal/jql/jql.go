// Package jql evaluates the JQL subset gadak and typical clients send.
//
// Supported:
//
//	project = KEY | project in (A, B)
//	key = KEY | key in (...)
//	updated/created >= / > / <= / < timestamp
//	status / statusCategory / issuetype / type / priority / assignee / reporter
//	AND / OR / parentheses
//	ORDER BY updated|created|key ASC|DESC
//
// An empty query or a bare ORDER BY matches every issue. Unparseable JQL
// is an error — returning every row would look like a working search.
package jql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/midagedev/issuetap/internal/model"
)

// Query is a parsed JQL.
type Query struct {
	Root  node
	Order []Order
	Raw   string
}

// Order is one ORDER BY term.
type Order struct {
	Field string
	Desc  bool
}

type node interface {
	match(ctx evalCtx, iss *model.Issue) bool
}

type evalCtx struct {
	projectOf  func(string) string
	statusOf   func(*model.Issue) (id, name, cat string)
	typeOf     func(*model.Issue) (id, name string)
	priorityOf func(*model.Issue) (id, name string)
	userOf     func(id string) *model.User
	location   *time.Location
}

// Lookup is what the store supplies so JQL can resolve ids to names
// (and the other way) without importing the store package.
type Lookup struct {
	Status     func(id string) *model.Status
	IssueType  func(id string) *model.IssueType
	Priority   func(id string) *model.Priority
	User       func(id string) *model.User
	Location   *time.Location
}

type cmpOp string

const (
	opEq  cmpOp = "="
	opNeq cmpOp = "!="
	opGT  cmpOp = ">"
	opGTE cmpOp = ">="
	opLT  cmpOp = "<"
	opLTE cmpOp = "<="
	opIn  cmpOp = "in"
	opNin cmpOp = "not in"
)

type pred struct {
	field string
	op    cmpOp
	vals  []string
}

type andN struct{ kids []node }
type orN struct{ kids []node }
type notN struct{ kid node }

func (a andN) match(ctx evalCtx, iss *model.Issue) bool {
	for _, k := range a.kids {
		if !k.match(ctx, iss) {
			return false
		}
	}
	return true
}
func (o orN) match(ctx evalCtx, iss *model.Issue) bool {
	for _, k := range o.kids {
		if k.match(ctx, iss) {
			return true
		}
	}
	return false
}
func (n notN) match(ctx evalCtx, iss *model.Issue) bool { return !n.kid.match(ctx, iss) }

func (p pred) match(ctx evalCtx, iss *model.Issue) bool {
	got := p.values(ctx, iss)
	switch p.op {
	case opIn:
		return anyIn(got, p.vals)
	case opNin:
		return !anyIn(got, p.vals)
	case opEq:
		return anyIn(got, p.vals)
	case opNeq:
		return !anyIn(got, p.vals)
	case opGT, opGTE, opLT, opLTE:
		if len(p.vals) == 0 {
			return false
		}
		left, ok1 := parseTime(gotTime(got), ctx.location)
		right, ok2 := parseTime(p.vals[0], ctx.location)
		if !ok1 || !ok2 {
			return false
		}
		switch p.op {
		case opGT:
			return left.After(right)
		case opGTE:
			return !left.Before(right)
		case opLT:
			return left.Before(right)
		case opLTE:
			return !left.After(right)
		}
	}
	return false
}

func (p pred) values(ctx evalCtx, iss *model.Issue) []string {
	switch p.field {
	case "project":
		return []string{iss.ProjectKey}
	case "key":
		return []string{iss.Key}
	case "updated":
		return []string{iss.Updated}
	case "created":
		return []string{iss.Created}
	case "status":
		id, name, _ := ctx.statusOf(iss)
		return []string{id, name, strings.ToLower(name)}
	case "statuscategory":
		_, _, cat := ctx.statusOf(iss)
		return []string{cat, model.Category(cat)}
	case "issuetype", "type":
		id, name := ctx.typeOf(iss)
		return []string{id, name, strings.ToLower(name)}
	case "priority":
		id, name := ctx.priorityOf(iss)
		return []string{id, name, strings.ToLower(name)}
	case "assignee":
		return userVals(ctx, iss.AssigneeID)
	case "reporter":
		return userVals(ctx, iss.ReporterID)
	case "summary":
		return []string{iss.Summary}
	case "labels":
		return append([]string{}, iss.Labels...)
	}
	return nil
}

func userVals(ctx evalCtx, id string) []string {
	if id == "" {
		return []string{"", "unassigned", "null"}
	}
	out := []string{id}
	if u := ctx.userOf(id); u != nil {
		out = append(out, u.DisplayName, u.Email, u.Name, u.Key, u.AccountID)
	}
	return out
}

func anyIn(got, want []string) bool {
	for _, g := range got {
		for _, w := range want {
			if g == w || (g != "" && w != "" && strings.EqualFold(g, w)) {
				return true
			}
		}
	}
	return false
}

func gotTime(got []string) string {
	if len(got) == 0 {
		return ""
	}
	return got[0]
}

func parseTime(s string, loc *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	layouts := []string{
		model.JiraTime,
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006/01/02 15:04",
		"2006/01/02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"2006/01/02",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, true
		}
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Parse compiles JQL. An empty string is a match-all query.
func Parse(raw string) (Query, error) {
	raw = strings.TrimSpace(raw)
	q := Query{Raw: raw}
	if raw == "" {
		return q, nil
	}
	p := &parser{toks: lex(raw)}
	// Split ORDER BY off the end.
	body, order, err := p.splitOrder()
	if err != nil {
		return q, err
	}
	q.Order = order
	if strings.TrimSpace(body) == "" {
		return q, nil
	}
	p2 := &parser{toks: lex(body)}
	n, err := p2.parseOr()
	if err != nil {
		return q, err
	}
	if !p2.done() {
		return q, fmt.Errorf("jql: unexpected token %q", p2.peek().val)
	}
	q.Root = n
	return q, nil
}

// Filter returns the matching issues, already ordered. max < 0 means no cap.
func Filter(issues []*model.Issue, q Query, look Lookup, offset, max int) []*model.Issue {
	ctx := makeCtx(look)
	out := make([]*model.Issue, 0, len(issues))
	for _, iss := range issues {
		if q.Root == nil || q.Root.match(ctx, iss) {
			out = append(out, iss)
		}
	}
	sortIssues(out, q.Order)
	if offset < 0 {
		offset = 0
	}
	if offset > len(out) {
		return nil
	}
	out = out[offset:]
	if max >= 0 && max < len(out) {
		out = out[:max]
	}
	return out
}

// Count is Filter without paging.
func Count(issues []*model.Issue, q Query, look Lookup) int {
	return len(Filter(issues, q, look, 0, -1))
}

func makeCtx(look Lookup) evalCtx {
	loc := look.Location
	if loc == nil {
		loc = time.FixedZone("KST", 9*3600)
	}
	return evalCtx{
		location: loc,
		statusOf: func(iss *model.Issue) (string, string, string) {
			if look.Status != nil {
				if s := look.Status(iss.StatusID); s != nil {
					return s.ID, s.Name, s.StatusCategory.Key
				}
			}
			return iss.StatusID, iss.StatusID, "new"
		},
		typeOf: func(iss *model.Issue) (string, string) {
			if look.IssueType != nil {
				if t := look.IssueType(iss.IssueTypeID); t != nil {
					return t.ID, t.Name
				}
			}
			return iss.IssueTypeID, iss.IssueTypeID
		},
		priorityOf: func(iss *model.Issue) (string, string) {
			if look.Priority != nil {
				if p := look.Priority(iss.PriorityID); p != nil {
					return p.ID, p.Name
				}
			}
			return iss.PriorityID, iss.PriorityID
		},
		userOf: func(id string) *model.User {
			if look.User != nil {
				return look.User(id)
			}
			return nil
		},
	}
}

func sortIssues(issues []*model.Issue, order []Order) {
	if len(order) == 0 {
		// Default: updated desc, then key asc — Cloud's usual.
		order = []Order{{Field: "updated", Desc: true}, {Field: "key", Desc: false}}
	}
	// Insertion sort is fine: fixtures are small; determinism matters more.
	for i := 1; i < len(issues); i++ {
		j := i
		for j > 0 && less(issues[j], issues[j-1], order) {
			issues[j], issues[j-1] = issues[j-1], issues[j]
			j--
		}
	}
}

func less(a, b *model.Issue, order []Order) bool {
	for _, o := range order {
		av, bv := orderVal(a, o.Field), orderVal(b, o.Field)
		if av == bv {
			continue
		}
		if o.Desc {
			return av > bv
		}
		return av < bv
	}
	return a.Key < b.Key
}

func orderVal(iss *model.Issue, field string) string {
	switch strings.ToLower(field) {
	case "updated":
		if t, ok := parseTime(iss.Updated, nil); ok {
			return t.UTC().Format(time.RFC3339Nano)
		}
		return iss.Updated
	case "created":
		if t, ok := parseTime(iss.Created, nil); ok {
			return t.UTC().Format(time.RFC3339Nano)
		}
		return iss.Created
	case "key":
		return iss.Key
	}
	return iss.Key
}

// --- lexer / parser ----------------------------------------------------------

type kind int

const (
	kEOF kind = iota
	kIdent
	kString
	kOp
	kLParen
	kRParen
	kComma
)

type token struct {
	kind kind
	val  string
}

func lex(s string) []token {
	var out []token
	i := 0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			i += w
			continue
		}
		switch r {
		case '(':
			out = append(out, token{kLParen, "("})
			i++
		case ')':
			out = append(out, token{kRParen, ")"})
			i++
		case ',':
			out = append(out, token{kComma, ","})
			i++
		case '"', '\'':
			str, n, ok := readString(s[i:])
			if !ok {
				out = append(out, token{kIdent, s[i:]})
				i = len(s)
				break
			}
			out = append(out, token{kString, str})
			i += n
		case '=', '!', '>', '<':
			op, n := readOp(s[i:])
			out = append(out, token{kOp, op})
			i += n
		default:
			j := i
			for j < len(s) {
				rj, wj := utf8.DecodeRuneInString(s[j:])
				if unicode.IsSpace(rj) || strings.ContainsRune("(),=!<>'\"", rj) {
					break
				}
				j += wj
			}
			out = append(out, token{kIdent, s[i:j]})
			i = j
		}
	}
	out = append(out, token{kEOF, ""})
	return out
}

func readString(s string) (string, int, bool) {
	if s == "" {
		return "", 0, false
	}
	q := s[0]
	if q != '"' && q != '\'' {
		return "", 0, false
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] == q {
			return b.String(), i + 1, true
		}
		b.WriteByte(s[i])
		i++
	}
	return "", 0, false
}

func readOp(s string) (string, int) {
	if len(s) >= 2 {
		switch s[:2] {
		case "!=", ">=", "<=":
			return s[:2], 2
		}
	}
	return s[:1], 1
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token {
	if p.i >= len(p.toks) {
		return token{kEOF, ""}
	}
	return p.toks[p.i]
}
func (p *parser) next() token {
	t := p.peek()
	if t.kind != kEOF {
		p.i++
	}
	return t
}
func (p *parser) done() bool { return p.peek().kind == kEOF }

func (p *parser) takeIdent(want string) bool {
	t := p.peek()
	if t.kind == kIdent && strings.EqualFold(t.val, want) {
		p.next()
		return true
	}
	return false
}

func (p *parser) splitOrder() (string, []Order, error) {
	// Reconstruct raw-ish from tokens; find ORDER BY.
	idx := -1
	for i := 0; i < len(p.toks)-1; i++ {
		if p.toks[i].kind == kIdent && strings.EqualFold(p.toks[i].val, "order") &&
			p.toks[i+1].kind == kIdent && strings.EqualFold(p.toks[i+1].val, "by") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return joinToks(p.toks[:len(p.toks)-1]), nil, nil
	}
	body := joinToks(p.toks[:idx])
	rest := p.toks[idx+2:]
	var order []Order
	for len(rest) > 0 && rest[0].kind != kEOF {
		if rest[0].kind != kIdent && rest[0].kind != kString {
			return body, nil, fmt.Errorf("jql: ORDER BY expected field, got %q", rest[0].val)
		}
		field := strings.ToLower(rest[0].val)
		rest = rest[1:]
		desc := false
		if len(rest) > 0 && rest[0].kind == kIdent {
			switch strings.ToLower(rest[0].val) {
			case "desc":
				desc = true
				rest = rest[1:]
			case "asc":
				rest = rest[1:]
			}
		}
		order = append(order, Order{Field: field, Desc: desc})
		if len(rest) > 0 && rest[0].kind == kComma {
			rest = rest[1:]
		}
	}
	return body, order, nil
}

func joinToks(ts []token) string {
	var b strings.Builder
	for i, t := range ts {
		if t.kind == kEOF {
			continue
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		if t.kind == kString {
			b.WriteByte('"')
			b.WriteString(t.val)
			b.WriteByte('"')
			continue
		}
		b.WriteString(t.val)
	}
	return b.String()
}

func (p *parser) parseOr() (node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.takeIdent("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		if o, ok := left.(orN); ok {
			o.kids = append(o.kids, right)
			left = o
		} else {
			left = orN{kids: []node{left, right}}
		}
	}
	return left, nil
}

func (p *parser) parseAnd() (node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.takeIdent("and") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		if a, ok := left.(andN); ok {
			a.kids = append(a.kids, right)
			left = a
		} else {
			left = andN{kids: []node{left, right}}
		}
	}
	return left, nil
}

func (p *parser) parseNot() (node, error) {
	if p.takeIdent("not") {
		k, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return notN{kid: k}, nil
	}
	return p.parseAtom()
}

func (p *parser) parseAtom() (node, error) {
	if p.peek().kind == kLParen {
		p.next()
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != kRParen {
			return nil, fmt.Errorf("jql: missing )")
		}
		p.next()
		return n, nil
	}
	return p.parsePred()
}

func (p *parser) parsePred() (node, error) {
	t := p.next()
	if t.kind != kIdent && t.kind != kString {
		return nil, fmt.Errorf("jql: expected field, got %q", t.val)
	}
	field := strings.ToLower(t.val)
	// not in
	if p.takeIdent("not") {
		if !p.takeIdent("in") {
			return nil, fmt.Errorf("jql: expected IN after NOT")
		}
		vals, err := p.listOrVal()
		if err != nil {
			return nil, err
		}
		return pred{field: field, op: opNin, vals: vals}, nil
	}
	if p.takeIdent("in") {
		vals, err := p.listOrVal()
		if err != nil {
			return nil, err
		}
		return pred{field: field, op: opIn, vals: vals}, nil
	}
	op := p.next()
	var c cmpOp
	switch op.val {
	case "=":
		c = opEq
	case "!=":
		c = opNeq
	case ">":
		c = opGT
	case ">=":
		c = opGTE
	case "<":
		c = opLT
	case "<=":
		c = opLTE
	default:
		return nil, fmt.Errorf("jql: expected operator after %s, got %q", field, op.val)
	}
	val, err := p.value()
	if err != nil {
		return nil, err
	}
	return pred{field: field, op: c, vals: []string{val}}, nil
}

func (p *parser) listOrVal() ([]string, error) {
	if p.peek().kind != kLParen {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		return []string{v}, nil
	}
	p.next()
	var vals []string
	for {
		if p.peek().kind == kRParen {
			p.next()
			return vals, nil
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
		if p.peek().kind == kComma {
			p.next()
			continue
		}
		if p.peek().kind == kRParen {
			p.next()
			return vals, nil
		}
		return nil, fmt.Errorf("jql: expected , or ) in list")
	}
}

func (p *parser) value() (string, error) {
	t := p.next()
	switch t.kind {
	case kString, kIdent:
		if strings.EqualFold(t.val, "empty") || strings.EqualFold(t.val, "null") {
			return "", nil
		}
		return t.val, nil
	default:
		// numbers lex as ident; operators shouldn't land here
		if t.val != "" {
			if _, err := strconv.Atoi(t.val); err == nil {
				return t.val, nil
			}
		}
		return "", fmt.Errorf("jql: expected value, got %q", t.val)
	}
}
