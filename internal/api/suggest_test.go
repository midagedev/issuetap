package api

import "testing"

func TestSuggestFromUsersSearch(t *testing.T) {
	got := SuggestImplemented("GET", "/rest/api/3/users/search")
	if got != "GET /rest/api/3/user/search" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestFromDistantPathEmpty(t *testing.T) {
	if got := SuggestImplemented("GET", "/rest/api/3/zzz/qqq"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSuggestFromMethodOnlySearchJQL(t *testing.T) {
	got := SuggestImplemented("GET", "/rest/api/3/search/jql")
	if got != "POST /rest/api/3/search/jql" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestFromVersionSubstitution(t *testing.T) {
	got := SuggestImplemented("GET", "/rest/api/2/users/search")
	if got != "GET /rest/api/2/user/search" {
		t.Fatalf("got %q", got)
	}
	got = SuggestImplemented("GET", "/rest/dev-status/latest/issue/sumary")
	if got != "GET /rest/dev-status/latest/issue/summary" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestFromKeepsNonVersionParams(t *testing.T) {
	got := SuggestImplemented("GET", "/rest/api/3/issue/TAP-1/comments")
	if got != "GET /rest/api/3/issue/{key}/comment" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestFromDashboardEmpty(t *testing.T) {
	if got := SuggestImplemented("GET", "/rest/api/3/dashboard"); got != "" {
		t.Fatalf("got %q, want empty (COMPATIBILITY.md envelope)", got)
	}
}

func TestSuggestFromDifferentMethodNearIsOmitted(t *testing.T) {
	// POST /users/search is a typo and a method miss. Only exact-path
	// method swaps are hinted; a combined miss is dropped.
	if got := SuggestImplemented("POST", "/rest/api/3/users/search"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestSuggestFromExactSameMethodIsOmitted(t *testing.T) {
	if got := SuggestImplemented("GET", "/rest/api/3/user/search"); got != "" {
		t.Fatalf("got %q, want empty (already that route)", got)
	}
}

func TestSuggestFromTieOmits(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/rest/api/{v}/user/search", Level: Supported},
		{Method: "GET", Path: "/rest/api/{v}/uses/search", Level: Supported},
	}
	got := suggestFrom(routes, "GET", "/rest/api/3/usex/search")
	if got != "" {
		t.Fatalf("got %q, want empty on a distance-1 tie", got)
	}
}

func TestSuggestFromMethodTieOmits(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/rest/api/{v}/search", Level: Partial},
		{Method: "POST", Path: "/rest/api/{v}/search", Level: Supported},
	}
	got := suggestFrom(routes, "PUT", "/rest/api/3/search")
	if got != "" {
		t.Fatalf("got %q, want empty when GET and POST both match exactly", got)
	}
}

func TestSuggestFromPrefersSameMethod(t *testing.T) {
	routes := []Route{
		{Method: "GET", Path: "/rest/api/{v}/user/search", Level: Supported},
		{Method: "POST", Path: "/rest/api/{v}/users/search", Level: Supported},
	}
	got := suggestFrom(routes, "GET", "/rest/api/3/users/search")
	if got != "GET /rest/api/3/user/search" {
		t.Fatalf("got %q, want same-method near-match over exact other-method", got)
	}
}

func TestLevenshteinUsersUser(t *testing.T) {
	if d := levenshtein("users", "user"); d != 1 {
		t.Fatalf("users↔user dist=%d", d)
	}
	if d := levenshtein("dashboard", "board"); d != 4 {
		t.Fatalf("dashboard↔board dist=%d", d)
	}
	if d := levenshtein("search", "search"); d != 0 {
		t.Fatalf("identical dist=%d", d)
	}
}
