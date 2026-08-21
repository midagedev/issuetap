package store

// Generated agent aliases (gadak GDK-593): an X-Issuetap-Actor slug with
// no X-Issuetap-Actor-Name used to render as its raw slug, so a UI full of
// "claude:<uuid>" accounts could not tell one bot from another. A nameless
// slug now gets a deterministic adjective×animal alias ("Brisk Otter"),
// suffixed with the harness it came from when the prefix is known
// ("Brisk Otter (Claude Code)"). Named actors and pre-existing users are
// never rewritten.
//
// | Contract | Test |
// | --- | --- |
// | a nameless slug gets a Title Case two-word alias, not the slug | TestActorAliasFormat |
// | the same slug maps to the same alias across stores and repeat calls | TestActorAliasIsDeterministic |
// | known harness prefixes append their label; unknown prefixes append nothing | TestActorAliasHarnessSuffix |
// | an explicit X-Issuetap-Actor-Name wins and is never rewritten | TestActorAliasExplicitNameWins |
// | a pre-existing slug-named account (pre-GDK-593 shape) is not rewritten | TestActorAliasDoesNotMigrateExistingUsers |
// | a base name already held by another account gets a numeric discriminator | TestActorAliasConflictGetsNumericSuffix |
// | the assigned alias persists as an ordinary DisplayName across a persist round trip | TestActorAliasSurvivesPersistRoundTrip |
// | the dictionaries are 100 lowercase single words, unique per list | TestActorNameDictionaries |

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/midagedev/issuetap/internal/fixtures"
	"github.com/midagedev/issuetap/internal/locale"
)

// aliasPattern is the full generated-alias shape: "Adj Noun", an optional
// numeric discriminator, and an optional " (Harness)" label.
var aliasPattern = regexp.MustCompile(`^[A-Z][a-z]+ [A-Z][a-z]+( \d+)?( \([A-Za-z0-9 ]+\))?$`)

func tinyStore(t *testing.T) *Store {
	t.Helper()
	st := New(Options{Seed: 1, Locale: locale.EN})
	doc, err := fixtures.Load(fixtures.Example("tiny.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Apply(doc); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestActorAliasFormat: without a name, the alias is two Title Case words
// drawn from the dictionaries — never the slug itself.
func TestActorAliasFormat(t *testing.T) {
	st := tinyStore(t)
	defer st.Close()
	for _, slug := range []string{"claude:354bff2b", "grok:tars", "zeta:9f3a", "hermes:olymp", "claude:9c2d71e0"} {
		u := st.EnsureActor(slug, "")
		t.Logf("%s -> %s", slug, u.DisplayName)
		if u.DisplayName == slug {
			t.Fatalf("slug %q: displayName is the raw slug; want a generated alias", slug)
		}
		if !aliasPattern.MatchString(u.DisplayName) {
			t.Fatalf("slug %q: displayName %q does not match Adj Noun [n] [(Harness)]", slug, u.DisplayName)
		}
		if u.AccountType != "agent" {
			t.Fatalf("slug %q: accountType=%q, want agent", slug, u.AccountType)
		}
	}
}

// TestActorAliasIsDeterministic: the alias is a pure function of the slug —
// two stores agree, and a repeated call neither renames nor duplicates.
func TestActorAliasIsDeterministic(t *testing.T) {
	a := tinyStore(t)
	defer a.Close()
	b := tinyStore(t)
	defer b.Close()
	for _, slug := range []string{"claude:354bff2b", "grok:tars", "hermes:olymp"} {
		first := a.EnsureActor(slug, "")
		second := b.EnsureActor(slug, "")
		if first.DisplayName != second.DisplayName {
			t.Fatalf("slug %q: two stores assigned %q and %q; want the same alias", slug, first.DisplayName, second.DisplayName)
		}
		again := a.EnsureActor(slug, "")
		if again.DisplayName != first.DisplayName {
			t.Fatalf("slug %q: repeat call renamed %q to %q", slug, first.DisplayName, again.DisplayName)
		}
	}
	if got := len(a.Users()); got != 5 {
		// tiny.yaml has Ada and Dana; the three slugs add one user each.
		t.Fatalf("users=%d, want 2 fixture + 3 agent users", got)
	}
}

// TestActorAliasHarnessSuffix: the prefix map is one table — claude: names
// its harness, grok/hermes prefixes name theirs, anything else gets no
// suffix at all.
func TestActorAliasHarnessSuffix(t *testing.T) {
	st := tinyStore(t)
	defer st.Close()
	cases := []struct{ slug, suffix string }{
		{"claude:354bff2b", " (Claude Code)"},
		{"claude:9c2d71e0", " (Claude Code)"},
		{"grok:tars", " (Grok)"},
		{"grok-run-12", " (Grok)"},
		{"hermes:olymp", " (Hermes)"},
		{"zeta:9f3a", ""},
		{"claude", ""}, // the claude entry keys on "claude:" — bare "claude" is unknown
	}
	for _, tc := range cases {
		u := st.EnsureActor(tc.slug, "")
		if tc.suffix == "" {
			if strings.Contains(u.DisplayName, "(") {
				t.Fatalf("slug %q: displayName %q carries a harness suffix; want none", tc.slug, u.DisplayName)
			}
			continue
		}
		if !strings.HasSuffix(u.DisplayName, tc.suffix) {
			t.Fatalf("slug %q: displayName %q lacks suffix %q", tc.slug, u.DisplayName, tc.suffix)
		}
	}
}

// TestActorAliasExplicitNameWins: X-Issuetap-Actor-Name is used verbatim and
// a later nameless request must not replace it with a generated alias.
func TestActorAliasExplicitNameWins(t *testing.T) {
	st := tinyStore(t)
	defer st.Close()
	if u := st.EnsureActor("claude:354bff2b", "Claude (build 1)"); u.DisplayName != "Claude (build 1)" {
		t.Fatalf("explicit name=%q, want it verbatim", u.DisplayName)
	}
	if u := st.EnsureActor("claude:354bff2b", ""); u.DisplayName != "Claude (build 1)" {
		t.Fatalf("repeat without name: displayName=%q, want the explicit name kept", u.DisplayName)
	}
}

// TestActorAliasDoesNotMigrateExistingUsers: accounts created before
// GDK-593 carry the slug as their display name. They exist under the slug
// key, so EnsureActor returns them untouched — rewriting them is the
// lead's separate call, not this feature.
func TestActorAliasDoesNotMigrateExistingUsers(t *testing.T) {
	st := tinyStore(t)
	defer st.Close()
	st.Apply(fixtures.Doc{Users: []fixtures.User{
		{AccountID: "claude:legacy", DisplayName: "claude:legacy", AccountType: "agent"},
	}})
	u := st.EnsureActor("claude:legacy", "")
	if u.DisplayName != "claude:legacy" {
		t.Fatalf("pre-existing slug-named user rewritten: displayName=%q", u.DisplayName)
	}
}

// TestActorAliasConflictGetsNumericSuffix: 10,000 combinations make this
// rare, but when the generated base is already another account's display
// name the new slug gets "Base 2", "Base 3", … — the number sits before
// the harness suffix.
func TestActorAliasConflictGetsNumericSuffix(t *testing.T) {
	// Discover what the slug is assigned, then pre-claim exactly that
	// display name under a different account. Conflict compares the full
	// assigned name, suffix included — that is the string the UI shows.
	probe := tinyStore(t)
	full := probe.EnsureActor("grok:collide", "").DisplayName
	probe.Close()
	if !strings.HasSuffix(full, " (Grok)") {
		t.Fatalf("probe assigned %q, want a (Grok)-suffixed name", full)
	}

	st := tinyStore(t)
	defer st.Close()
	st.Apply(fixtures.Doc{Users: []fixtures.User{
		{AccountID: "5b10a2844c20165700ede77g", DisplayName: full},
	}})
	u := st.EnsureActor("grok:collide", "")
	want := strings.TrimSuffix(full, " (Grok)") + " 2 (Grok)"
	if u.DisplayName != want {
		t.Fatalf("conflict: displayName=%q, want %q", u.DisplayName, want)
	}
}

// TestActorAliasSurvivesPersistRoundTrip: the alias is stored as an
// ordinary DisplayName, so the persistence file carries it and a re-opened
// store keeps answering with the same alias for the same slug.
func TestActorAliasSurvivesPersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.yaml")
	st, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path, PersistDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	assigned := st.EnsureActor("claude:354bff2b", "").DisplayName
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(Options{Seed: 1, Locale: locale.EN, PersistPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	// The reloaded user must already carry the alias — the fresh call below
	// must find it, not regenerate it.
	found := ""
	for _, u := range st2.Users() {
		if u.AccountID == "claude:354bff2b" {
			found = u.DisplayName
		}
	}
	if found != assigned {
		t.Fatalf("persisted displayName=%q, want the assigned alias %q", found, assigned)
	}
	if u := st2.EnsureActor("claude:354bff2b", ""); u.DisplayName != assigned {
		t.Fatalf("post-reload EnsureActor displayName=%q, want %q", u.DisplayName, assigned)
	}
}

// TestActorNameDictionaries: the word lists are the alias contract —
// exactly 100 lowercase single words each, no duplicates, so the name
// space stays 10,000 combinations and the shape test above can rely on
// Title Case mapping cleanly.
func TestActorNameDictionaries(t *testing.T) {
	word := regexp.MustCompile(`^[a-z]+$`)
	for name, list := range map[string][]string{
		"actorAdjectives": actorAdjectives,
		"actorNouns":      actorNouns,
	} {
		if len(list) != 100 {
			t.Errorf("%s: %d entries, want 100", name, len(list))
		}
		seen := map[string]bool{}
		for _, w := range list {
			if !word.MatchString(w) {
				t.Errorf("%s: %q is not a lowercase single word", name, w)
			}
			if seen[w] {
				t.Errorf("%s: duplicate %q", name, w)
			}
			seen[w] = true
		}
	}
	for _, adj := range actorAdjectives {
		for _, noun := range actorNouns {
			if adj == noun {
				t.Errorf("word %q appears in both dictionaries", adj)
			}
		}
	}
}
