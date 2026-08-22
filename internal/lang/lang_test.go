package lang

import (
	"os"
	"strings"
	"testing"
)

// Every name Bazarr can send must be a name subsarr stores, or the subtitles in
// that language are unreachable no matter how good the search is.
func TestCanonical_EveryBazarrNameIsStoredVerbatim(t *testing.T) {
	names := Names()
	if len(names) < 90 {
		t.Fatalf("only %d canonical languages, want at least the 90 from Bazarr's converter", len(names))
	}
	for _, name := range names {
		if got := Canonical(name); got != name {
			t.Errorf("Canonical(%q) = %q, want the name unchanged", name, got)
		}
		if !Known(name) {
			t.Errorf("Known(%q) = false, want true", name)
		}
	}
}

func TestRequestable_SeparatesBazarrNamesFromDumpOnlyNames(t *testing.T) {
	if !Requestable("brazillian-portuguese") {
		t.Error("brazillian-portuguese should be requestable by Bazarr")
	}
	if Requestable("big_5_code") {
		t.Error("big_5_code is not in Bazarr's converter and should not be requestable")
	}
	if _, ok := Codes("chinese-bg-code"); !ok {
		t.Error("chinese-bg-code should carry a babelfish code")
	}
}

// The two languages the issue calls out as silently unreachable, plus the
// spellings the dump and an operator are each likely to use for them.
func TestCanonical_SpellingVariants(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Brazilian Portuguese", "brazillian-portuguese"},
		{"brazillian-portuguese", "brazillian-portuguese"},
		{"brazilian_portuguese", "brazillian-portuguese"},
		{"BRAZILIAN PORTUGUESE", "brazillian-portuguese"},
		{"Chinese BG code", "chinese-bg-code"},
		{"chinese_bg_code", "chinese-bg-code"},
		{"chinese-bg-code", "chinese-bg-code"},
		{"Farsi/Persian", "farsi_persian"},
		{"farsi persian", "farsi_persian"},
		{"Khmer", "cambodian-khmer"},
		{"Pushto", "pashto"},
		{"Espranto", "esperanto"},
		{"Ukrainian", "ukranian"},
		{" English ", "english"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// `Farsi/Persian` reaches Canonical through a separator the match key folds; make
// sure the fold is not doing it by accident of the alias table.
func TestCanonical_IsIdempotent(t *testing.T) {
	for _, name := range Names() {
		once := Canonical(name)
		if twice := Canonical(once); twice != once {
			t.Errorf("Canonical(%q) = %q but Canonical(%q) = %q", name, once, once, twice)
		}
	}
}

// The language column of an existing installation is polluted by a file-name
// parser that split on the wrong separator. Every value it produced must land on
// a real language, or those rows stay unreachable after the migration.
func TestCanonical_RecoversPollutedDumpLanguages(t *testing.T) {
	data, err := os.ReadFile("testdata/dump_languages.txt")
	if err != nil {
		t.Fatalf("read observed languages: %v", err)
	}

	var unresolved []string
	for _, observed := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		observed = strings.TrimSpace(observed)
		if observed == "" {
			continue
		}
		if !Known(observed) {
			unresolved = append(unresolved, observed+" → "+Canonical(observed))
		}
	}
	if len(unresolved) > 0 {
		t.Errorf("%d observed languages do not resolve to a known language:\n  %s",
			len(unresolved), strings.Join(unresolved, "\n  "))
	}
}

func TestCanonical_PollutionPrefixes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2_english", "english"},
		{"3_indonesian", "indonesian"},
		{"2_HI_english", "english"},
		{"2_HI_big_5_code", "big_5_code"},
		{"ii_your_sister_is_a_werewolf_english", "english"},
		{"tai-ai-qing_english", "english"},
		{"to-see-pne-se_italian", "italian"},
		// A dual-language name is a language of its own: its prefix is itself a
		// language, which is what tells it apart from a mis-split file name.
		{"english-german", "english-german"},
		{"dutch-english", "dutch-english"},
		{"bulgarian-english", "bulgarian-english"},
		{"hungarian_english", "hungarian_english"},
	}
	for _, tc := range tests {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An unknown language must not collide with a real one, and must fold the same
// way every time so its rows stay together.
func TestCanonical_UnknownLanguageFoldsDeterministically(t *testing.T) {
	got := Canonical("Klingon_Dialect")
	if got != "klingon-dialect" {
		t.Errorf("Canonical = %q, want %q", got, "klingon-dialect")
	}
	if Known(got) {
		t.Error("an invented language should not be Known")
	}
	if Canonical(got) != got {
		t.Error("folding an unknown language should be idempotent")
	}
}

func TestParseList(t *testing.T) {
	t.Run("accepts every spelling the canonicaliser understands", func(t *testing.T) {
		got, err := ParseList("Brazilian Portuguese, brazillian-portuguese , danish,English")
		if err != nil {
			t.Fatalf("ParseList: %v", err)
		}
		want := []string{"brazillian-portuguese", "danish", "english"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v (duplicates should collapse)", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("empty list means every language", func(t *testing.T) {
		got, err := ParseList("")
		if err != nil || len(got) != 0 {
			t.Errorf("ParseList(\"\") = %v, %v; want empty, nil", got, err)
		}
	})

	t.Run("a typo is an error, not a filter that matches nothing", func(t *testing.T) {
		_, err := ParseList("danish,dansk")
		if err == nil {
			t.Fatal("expected an error for an unknown language")
		}
		if !strings.Contains(err.Error(), "dansk") {
			t.Errorf("error %q should name the offending value", err)
		}
	})
}

// The archive's file names are cut off at the filesystem's path limit, which
// leaves the language spelled halfway. A fragment only one language begins with
// is that language.
func TestCanonical_RecoversTruncatedNames(t *testing.T) {
	tests := []struct{ in, want string }{
		{"englis", "english"},
		{"indone", "indonesian"},
		{"kurdis", "kurdish"},
		{"sinhal", "sinhala"},
		{"brazillian-p", "brazillian-portuguese"},
		{"farsi_", "farsi_persian"},
		{"norwegia", "norwegian"},
	}
	for _, tc := range tests {
		if got := Canonical(tc.in); got != tc.want {
			t.Errorf("Canonical(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A fragment several languages share says nothing, and guessing between them
// would file subtitles under a language nobody asked for.
func TestCanonical_LeavesAmbiguousFragmentsAlone(t *testing.T) {
	for _, ambiguous := range []string{
		"ar",   // too short to mean anything
		"sin",  // sindhi, sinhala — and too short either way
		"turk", // turkish, turkmen: same length, a real tie
		"s",
	} {
		if got := Canonical(ambiguous); Known(got) {
			t.Errorf("Canonical(%q) = %q, want it left as an unknown language", ambiguous, got)
		}
	}
}

// Prefix recovery is a last resort: it must never change a name that already
// resolves, or a language could quietly move.
func TestCanonical_PrefixRecoveryNeverOverridesAKnownName(t *testing.T) {
	for _, name := range Names() {
		if got := Canonical(name); got != name {
			t.Errorf("Canonical(%q) = %q — prefix recovery changed a known name", name, got)
		}
	}
	for alias, want := range map[string]string{
		"Brazilian Portuguese": "brazillian-portuguese",
		"Khmer":                "cambodian-khmer",
		"chinese":              "chinese-bg-code",
		"portuguese":           "portuguese",
		"english":              "english",
	} {
		if got := Canonical(alias); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", alias, got, want)
		}
	}
}
