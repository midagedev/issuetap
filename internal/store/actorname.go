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
//
// The dictionaries render per locale: a ko store draws the alias from
// Korean lists ("씩씩한 수달"), every other locale from the English ones.
// The hash is locale-independent — the same slug lands on the same
// adjective×noun pair in any locale; only the words differ. The locale at
// creation time decides, and the stored name is never re-rendered when the
// locale changes later.

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/midagedev/issuetap/internal/locale"
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

// actorAdjectivesKO and actorNounsKO are the Korean alias dictionaries:
// 100 attributive-form adjectives ("씩씩한", "재빠른") and 100 common animal
// nouns ("수달", "고라니") — plain everyday words, no brands, proper nouns,
// or words that read as insults. The words arrive fully inflected, so the
// Korean alias needs no case folding. There are no ja/de dictionaries on
// purpose: a ja or de store falls back to the English lists above. Shape
// is pinned by TestActorNameDictionariesKO; the locale-independent index
// derivation by TestActorAliasKoreanSharesIndexWithEnglish.
var actorAdjectivesKO = []string{
	"씩씩한", "재빠른", "포근한", "다정한", "밝은", "맑은", "눈부신", "반짝이는", "빛나는", "화사한",
	"찬란한", "환한", "따뜻한", "아늑한", "부드러운", "상냥한", "온화한", "정다운", "훈훈한", "고즈넉한",
	"조용한", "고요한", "평화로운", "잔잔한", "느긋한", "여유로운", "한가로운", "차분한", "깔끔한", "깨끗한",
	"말끔한", "단정한", "용감한", "대담한", "과감한", "당당한", "굳센", "강인한", "든든한", "성실한",
	"부지런한", "근면한", "침착한", "꾸준한", "진지한", "진솔한", "정직한", "겸허한", "정중한", "친절한",
	"바른", "곧은", "굳건한", "믿음직한", "자상한", "신중한", "지혜로운", "슬기로운", "총명한", "영리한",
	"똑똑한", "민첩한", "날랜", "빠른", "신속한", "귀여운", "사랑스러운", "고마운", "즐거운", "기쁜",
	"행복한", "유쾌한", "명랑한", "쾌활한", "활기찬", "활발한", "발랄한", "천진한", "순수한", "시원한",
	"선선한", "쾌청한", "상쾌한", "청량한", "서늘한", "따사로운", "촉촉한", "매끄러운", "우아한", "멋진",
	"근사한", "훌륭한", "뛰어난", "탁월한", "유능한", "능숙한", "건강한", "싱싱한", "자연스러운", "새로운",
}

var actorNounsKO = []string{
	"수달", "고라니", "두루미", "다람쥐", "노루", "사슴", "산양", "하마", "코끼리", "기린",
	"얼룩말", "코알라", "캥거루", "판다", "여우", "늑대", "곰", "오소리", "너구리", "토끼",
	"원숭이", "침팬지", "고릴라", "오랑우탄", "미어캣", "카피바라", "알파카", "라마", "낙타", "당나귀",
	"망아지", "송아지", "강아지", "고양이", "양", "염소", "사자", "호랑이", "표범", "치타",
	"고래", "돌고래", "범고래", "상어", "물개", "바다사자", "바다표범", "문어", "오징어", "해파리",
	"불가사리", "말미잘", "해마", "거북이", "자라", "악어", "도마뱀", "이구아나", "카멜레온", "개구리",
	"두꺼비", "도롱뇽", "참새", "비둘기", "까치", "까마귀", "오리", "거위", "백조", "펭귄",
	"갈매기", "제비", "올빼미", "독수리", "매", "솔개", "뻐꾸기", "앵무새", "물총새", "나비",
	"호랑나비", "잠자리", "매미", "귀뚜라미", "무당벌레", "반딧불이", "메뚜기", "사마귀", "장수풍뎅이", "사슴벌레",
	"달팽이", "거미", "풍뎅이", "고슴도치", "두더지", "박쥐", "흰동가리", "붕어", "잉어", "북극곰",
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
	// The dictionaries follow the store locale, the hash does not: the
	// same slug lands on the same adjective×noun pair in every locale and
	// only the rendering differs. s.loc is read directly because the
	// caller holds s.mu — Locale would re-lock it.
	korean := s.loc == locale.KO
	adjectives, nouns := actorAdjectives, actorNouns
	if korean {
		adjectives, nouns = actorAdjectivesKO, actorNounsKO
	}
	i := v % uint64(len(adjectives))
	j := (v / uint64(len(adjectives))) % uint64(len(nouns))
	var base string
	if korean {
		// Korean words carry their own attributive form; titleFirst is one
		// ASCII byte by contract and must not touch them.
		base = adjectives[i] + " " + nouns[j]
	} else {
		base = titleFirst(adjectives[i]) + " " + titleFirst(nouns[j])
	}
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
	for _, u := range s.usersLocked() {
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
