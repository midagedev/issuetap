package faults

import (
	"testing"
)

func TestRevokeAfterN(t *testing.T) {
	e := New([]Fault{{
		Name: "revoke", After: 3, Status: 401, PathPrefix: "/rest/",
	}})
	for i := 1; i <= 2; i++ {
		h := e.Apply("GET", "/rest/api/3/myself")
		if h.Skip {
			t.Fatalf("request %d should pass", i)
		}
	}
	h := e.Apply("GET", "/rest/api/3/myself")
	if !h.Skip || h.Fault == nil || h.Fault.Status != 401 {
		t.Fatalf("request 3 should 401, got %+v", h)
	}
}

func Test429Once(t *testing.T) {
	e := New([]Fault{{
		Name: "burst", Times: 1, Status: 429, RetryAfter: 1, PathContains: "search",
	}})
	h := e.Apply("POST", "/rest/api/3/search/jql")
	if !h.Skip || h.Fault.Status != 429 {
		t.Fatal(h)
	}
	h = e.Apply("POST", "/rest/api/3/search/jql")
	if h.Skip {
		t.Fatal("second search should pass")
	}
}

func TestPathFilter(t *testing.T) {
	e := New([]Fault{{Name: "wiki", Status: 401, PathPrefix: "/wiki/"}})
	if e.Apply("GET", "/rest/api/3/myself").Skip {
		t.Fatal("jira should pass")
	}
	if !e.Apply("GET", "/wiki/rest/api/space").Skip {
		t.Fatal("wiki should 401")
	}
}
