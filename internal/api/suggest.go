package api

import "strings"

// maxSuggestEditDistance is the largest Levenshtein distance at which a
// single fixed path segment still counts as a sibling. users↔user is 1.
// A wrong hint is worse than none, so this stays at 1.
const maxSuggestEditDistance = 1

// SuggestImplemented returns a copy-pasteable "METHOD /path" sibling of an
// unimplemented request, or "". Candidates are Inventory rows whose Level is
// not Unsupported. A hint is emitted only when:
//
//   - the request and the candidate have the same number of path segments;
//   - {param} segments match any request segment;
//   - either exactly one fixed segment differs and its edit distance is
//     <= maxSuggestEditDistance (same HTTP method preferred), or the path
//     matches exactly and only the method differs;
//   - that rendered suggestion is unique. Ties yield "".
//
// {v} is replaced with the request's version segment; other {param} tokens
// stay in the pattern. Exact same-method matches are skipped (the client
// already asked for that route).
func SuggestImplemented(method, path string) string {
	return suggestFrom(Inventory(), method, path)
}

func suggestFrom(routes []Route, method, path string) string {
	method = strings.TrimSpace(method)
	path = strings.TrimRight(path, "/")
	if method == "" || path == "" || path == "/" {
		return ""
	}

	var sameMethod []string
	var otherExact []string
	for _, r := range routes {
		if r.Level == Unsupported {
			continue
		}
		fit := classifyPath(r.Path, path)
		if fit == fitNone {
			continue
		}
		phrase := r.Method + " " + renderSuggestedPath(r.Path, path)
		if strings.EqualFold(r.Method, method) {
			if fit == fitNear {
				sameMethod = append(sameMethod, phrase)
			}
			continue
		}
		if fit == fitExact {
			otherExact = append(otherExact, phrase)
		}
	}

	same := uniqueStrings(sameMethod)
	if len(same) == 1 {
		return same[0]
	}
	if len(same) > 1 {
		return ""
	}
	other := uniqueStrings(otherExact)
	if len(other) == 1 {
		return other[0]
	}
	return ""
}

type pathFit int

const (
	fitNone pathFit = iota
	fitExact
	fitNear
)

func classifyPath(pattern, path string) pathFit {
	if glob(pattern, path) {
		return fitExact
	}
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	ps := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(ps) {
		return fitNone
	}
	mismatches := 0
	mismatchDist := 0
	for i := range pp {
		if isParamSegment(pp[i]) {
			continue
		}
		d := segmentEditDistance(pp[i], ps[i])
		if d == 0 {
			continue
		}
		mismatches++
		mismatchDist = d
		if mismatches > 1 {
			return fitNone
		}
	}
	if mismatches == 1 && mismatchDist > 0 && mismatchDist <= maxSuggestEditDistance {
		return fitNear
	}
	return fitNone
}

func renderSuggestedPath(pattern, requestPath string) string {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	ps := strings.Split(strings.Trim(requestPath, "/"), "/")
	out := make([]string, len(pp))
	for i := range pp {
		if pp[i] == "{v}" && i < len(ps) && ps[i] != "" {
			out[i] = ps[i]
			continue
		}
		out[i] = pp[i]
	}
	return "/" + strings.Join(out, "/")
}

func isParamSegment(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

func segmentEditDistance(a, b string) int {
	if strings.EqualFold(a, b) {
		return 0
	}
	a, b = strings.ToLower(a), strings.ToLower(b)
	if absInt(len(a)-len(b)) > maxSuggestEditDistance {
		return absInt(len(a) - len(b))
	}
	return levenshtein(a, b)
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return len(b)
	}
	if b == "" {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = min(del, ins, sub)
		}
		prev = cur
	}
	return prev[len(b)]
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
