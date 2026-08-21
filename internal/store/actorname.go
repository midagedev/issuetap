package store

// Generated agent aliases (gadak GDK-593). An X-Issuetap-Actor slug with
// no X-Issuetap-Actor-Name used to become its own display name, so a UI
// filled with "claude:<uuid8>" accounts could not tell one bot from
// another. A nameless slug now gets a deterministic adjective×animal
// alias in the Docker-container style ("Brisk Otter"), with the harness
// appended when the slug prefix names one ("Brisk Otter (Claude Code)").
// The alias is a pure function of the slug: no randomness, no clock, so
// the same slug maps to the same alias on every server and every restart
// (the fixture determinism rule applies — AGENTS.md).
//
// The assignment only fires on creation of an unknown slug. A slug that
// arrives with a name, or one that already exists (including accounts
// persisted before GDK-593 with the slug as their name), keeps what it
// has — rewriting existing names is deliberately out of scope.

import (
	"hash/fnv"
	"strconv"
	"strings"
)

// actorAdjectives and actorNouns are the alias dictionaries: 100 lowercase
// single words each — common, neutral, pronounceable; no proper nouns,
// brands, person names, or words that read as insults. 100×100 keeps the
// name space at 10,000 combinations, collision-free at tens-of-users
// scale. Shape is pinned by TestActorNameDictionaries.
var actorAdjectives = []string{
	"brisk", "calm", "clear", "bright", "gentle", "quiet", "swift", "steady", "merry", "jolly",
	"sunny", "breezy", "mellow", "cozy", "snug", "fresh", "crisp", "smooth", "soft", "airy",
	"fluffy", "fuzzy", "silky", "velvety", "glossy", "shiny", "polished", "gleaming", "glowing", "radiant",
	"lucid", "vivid", "rosy", "golden", "silver", "tawny", "speckled", "spotted", "striped", "dappled",
	"quick", "rapid", "speedy", "nimble", "agile", "lively", "peppy", "perky", "bouncy", "springy",
	"ready", "handy", "deft", "keen", "kind", "sweet", "genial", "friendly", "happy", "pleasant",
	"courteous", "polite", "gracious", "patient", "honest", "loyal", "faithful", "trusty", "sturdy", "robust",
	"hearty", "sincere", "earnest", "thoughtful", "tidy", "grand", "noble", "bold", "brave", "daring",
	"plucky", "spirited", "eager", "curious", "playful", "frisky", "sleepy", "tiny", "giant", "misty",
	"dewy", "frosty", "snowy", "moonlit", "starlit", "sunlit", "tranquil", "serene", "hushed", "peaceful",
}

var actorNouns = []string{
	"otter", "fox", "badger", "hare", "rabbit", "deer", "elk", "moose", "bison", "yak",
	"camel", "llama", "gazelle", "antelope", "zebra", "giraffe", "hippo", "rhino", "elephant", "kangaroo",
	"koala", "wombat", "possum", "chinchilla", "capybara", "porcupine", "hedgehog", "hamster", "ferret", "lynx",
	"meerkat", "mongoose", "tiger", "lion", "leopard", "cheetah", "panda", "raccoon", "bear", "wolf",
	"coyote", "dolphin", "porpoise", "whale", "orca", "narwhal", "beluga", "seal", "walrus", "manatee",
	"dugong", "manta", "turtle", "tortoise", "octopus", "squid", "cuttlefish", "nautilus", "seahorse", "starfish",
	"jellyfish", "newt", "salamander", "gecko", "iguana", "lizard", "chameleon", "frog", "axolotl", "butterfly",
	"moth", "dragonfly", "damselfly", "ladybug", "beetle", "firefly", "cricket", "grasshopper", "mantis", "cicada",
	"bumblebee", "snail", "heron", "falcon", "hawk", "owl", "raven", "robin", "wren", "finch",
	"sparrow", "swallow", "penguin", "puffin", "pelican", "flamingo", "crane", "stork", "egret", "magpie",
}

// actorHarnesses maps a slug prefix onto the harness label appended to a
// generated alias. First match wins; "claude:" keys on the colon so a bare
// "claude" slug is not claimed. A slug matching no prefix gets no suffix.
var actorHarnesses = []struct{ prefix, label string }{
	{"claude:", "Claude Code"},
	{"grok", "Grok"},
	{"hermes", "Hermes"},
}

// actorAliasLocked returns the display name generated for a nameless slug.
// Deterministic: FNV-1a over the slug picks one adjective and one noun,
// then the matching harness label (if any) is appended. When the full name
// is already another account's display name, a numeric discriminator goes
// before the harness suffix ("Brisk Otter 2 (Claude Code)"). The scan is a
// users-map walk — tens-of-users scale by design; each failed candidate is
// held by exactly one account, so the loop terminates within len(users)+1
// tries. Call with s.mu held.
func (s *Store) actorAliasLocked(slug string) string {
	h := fnv.New64a()
	h.Write([]byte(slug))
	v := h.Sum64()
	base := titleFirst(actorAdjectives[v%uint64(len(actorAdjectives))]) + " " +
		titleFirst(actorNouns[(v/uint64(len(actorAdjectives)))%uint64(len(actorNouns))])
	suffix := ""
	for _, hx := range actorHarnesses {
		if strings.HasPrefix(slug, hx.prefix) {
			suffix = " (" + hx.label + ")"
			break
		}
	}
	for n := 0; ; n++ {
		name := base
		if n > 0 {
			name += " " + strconv.Itoa(n+1)
		}
		name += suffix
		if !s.displayNameTakenLocked(name, slug) {
			return name
		}
	}
}

// displayNameTakenLocked reports whether any account other than
// exceptAccountID already shows name as its display name. Call with s.mu
// held.
func (s *Store) displayNameTakenLocked(name, exceptAccountID string) bool {
	for _, u := range s.users {
		if u.AccountID != exceptAccountID && u.DisplayName == name {
			return true
		}
	}
	return false
}

// titleFirst upper-cases the first letter. Dictionary words are lowercase
// ASCII, so this is one byte; no unicode machinery needed.
func titleFirst(word string) string {
	if word == "" {
		return word
	}
	return strings.ToUpper(word[:1]) + word[1:]
}
