// Package lang canonicalises Subscene language names.
//
// Bazarr's subsarr provider converts a babelfish language into one exact string
// (see bazarr_languages.json, generated from its converter). A subtitle whose
// stored language is spelled differently is unreachable: Bazarr sends the name it
// knows, and anything else never matches. Every language that enters subsarr —
// from the catalogue, from an archive file name, from a request, from an
// operator's whitelist — therefore passes through Canonical first.
package lang

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed bazarr_languages.json
var bazarrLanguages []byte

// dumpOnly are language names that exist in the Subscene archive but that Bazarr
// cannot request. They are canonical in their own right: rows keep them, the
// language list reports them, and nothing folds them into a requestable name —
// a dual-language subtitle is not the same subtitle as a single-language one.
var dumpOnly = []string{
	"big_5_code",
	"bulgarian-english",
	"dutch-english",
	"english-german",
	"greenlandic",
	"hungarian_english",
	"manipuri",
	"unknown",
}

// aliases map correct or alternative spellings onto the name Bazarr uses. The
// keys are match keys (see matchKey), not canonical names.
var aliases = map[string]string{
	"brazilian portuguese":  "brazillian-portuguese",
	"portuguese brazilian":  "brazillian-portuguese",
	"portuguese br":         "brazillian-portuguese",
	"khmer":                 "cambodian-khmer",
	"cambodian":             "cambodian-khmer",
	"pushto":                "pashto",
	"espranto":              "esperanto",
	"ukrainian":             "ukranian",
	"chinese bg code":       "chinese-bg-code",
	"chinese":               "chinese-bg-code",
	"farsi":                 "farsi_persian",
	"persian":               "farsi_persian",
	"northern sami":         "northen-sami",
	"filipino":              "tagalog",
	"burmese myanmar":       "burmese",
	"serbian latin":         "serbian",
	"serbian cyrillic":      "serbian",
	"bosnian latin":         "bosnian",
	"portuguese portugal":   "portuguese",
	"spanish latin america": "spanish",
	"spanish spain":         "spanish",
}

var (
	once sync.Once
	// byKey maps a match key to the canonical name.
	byKey map[string]string
	// canonical is the set of canonical names.
	canonical map[string]struct{}
	// codes maps a canonical name to the babelfish code tuple Bazarr uses. Kept
	// so the vendored converter can be asserted against in tests.
	codes map[string][]string
)

func load() {
	var doc struct {
		Languages map[string][]string `json:"languages"`
	}
	if err := json.Unmarshal(bazarrLanguages, &doc); err != nil {
		panic("lang: vendored Bazarr language list is not valid JSON: " + err.Error())
	}

	byKey = make(map[string]string, len(doc.Languages)*2)
	canonical = make(map[string]struct{}, len(doc.Languages))
	codes = make(map[string][]string, len(doc.Languages))

	add := func(name string) {
		canonical[name] = struct{}{}
		byKey[matchKey(name)] = name
	}
	for name, code := range doc.Languages {
		add(name)
		codes[name] = code
	}
	for _, name := range dumpOnly {
		add(name)
	}
	for alias, name := range aliases {
		byKey[alias] = name
	}
}

// Canonical returns the canonical spelling of a language name. It never fails:
// a name that matches nothing folds to a deterministic slug of itself, so an
// unknown language keeps its own bucket instead of colliding with a real one.
func Canonical(name string) string {
	once.Do(load)

	key := matchKey(name)
	if key == "" {
		return ""
	}
	if c, ok := byKey[key]; ok {
		return c
	}

	// Names taken from archive file names carry the leftovers of a mis-split
	// file name: `2_english`, `2_HI_english`, `tai-ai-qing_english`. The real
	// language is the tail. Only reduce when the discarded prefix is not itself
	// a language, so `english-german` stays the dual-language name it is.
	words := strings.Fields(key)
	for i := 1; i < len(words); i++ {
		tail, head := strings.Join(words[i:], " "), strings.Join(words[:i], " ")
		c, ok := byKey[tail]
		if !ok {
			continue
		}
		if _, headIsLanguage := byKey[head]; headIsLanguage {
			break
		}
		return c
	}

	return strings.ReplaceAll(key, " ", "-")
}

// Known reports whether a name canonicalises to a language subsarr recognises,
// as opposed to folding to a slug of itself.
func Known(name string) bool {
	once.Do(load)
	_, ok := canonical[Canonical(name)]
	return ok
}

// Requestable reports whether Bazarr's converter can produce this name. Names
// that only exist in the dump are stored and served, but no Bazarr search will
// ever ask for them.
func Requestable(name string) bool {
	once.Do(load)
	_, ok := codes[Canonical(name)]
	return ok
}

// Names returns every canonical language name, sorted.
func Names() []string {
	once.Do(load)
	names := make([]string, 0, len(canonical))
	for name := range canonical {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Codes returns the babelfish code tuple Bazarr's converter maps a canonical
// name to, and whether the name is in the vendored list.
func Codes(name string) ([]string, bool) {
	once.Do(load)
	c, ok := codes[Canonical(name)]
	return c, ok
}

// ParseList parses a comma-separated language whitelist into canonical names.
// A value that is not a language subsarr knows is an error rather than a silent
// filter that would match nothing: the operator meant something by it.
func ParseList(list string) ([]string, error) {
	var (
		out     []string
		seen    = map[string]struct{}{}
		unknown []string
	)
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		name := Canonical(raw)
		if !Known(name) {
			unknown = append(unknown, raw)
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown language(s): %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

// matchKey folds the spellings that mean the same language onto one string:
// case, and the separators the dump, Bazarr and operators use interchangeably.
func matchKey(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	lastSep := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch r {
		case '-', '_', ' ', '.', '+', '/', '(', ')', ',':
			if !lastSep {
				b.WriteByte(' ')
				lastSep = true
			}
		default:
			b.WriteRune(r)
			lastSep = false
		}
	}
	return strings.TrimSpace(b.String())
}
