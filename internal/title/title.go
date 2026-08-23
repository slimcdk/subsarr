// Package title normalises work titles and the slugs Subscene filed them under.
//
// Bazarr sends a title as the user's media library spells it; Subscene stored a
// slug. Both sides go through Normalize so that "S.W.A.T.", "SWAT" and "swat" —
// or "Don't Look Up" and "dont-look-up" — are the same string before anything is
// matched or ranked.
package title

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// dottedAcronym matches a run of single letters separated by dots, as in
// "S.W.A.T." — the dots are punctuation between letters of one word, not word
// separators, so they must collapse rather than split.
var dottedAcronym = regexp.MustCompile(`\b(?:\p{L}\.){2,}`)

// yearSuffix matches the `-1994` that Subscene appends to a slug when it needs to
// tell two works of the same name apart.
var yearSuffix = regexp.MustCompile(`-((?:19|20)\d{2})$`)

// Normalize reduces a title or query to its comparable words, separated by single
// spaces: lowercase, apostrophes dropped, dotted acronyms collapsed, everything
// else that is not a letter or digit treated as a separator.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("'", "", "’", "", "`", "").Replace(s)
	s = dottedAcronym.ReplaceAllStringFunc(s, func(m string) string {
		return strings.ReplaceAll(m, ".", "")
	})

	var (
		b       strings.Builder
		pending bool
	)
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if pending && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pending = false
			b.WriteRune(r)
		default:
			pending = true
		}
	}
	return b.String()
}

// Words returns the normalised words of a title or query.
func Words(s string) []string {
	n := Normalize(s)
	if n == "" {
		return nil
	}
	return strings.Split(n, " ")
}

// Slugify renders a title the way Subscene slugs are spelled, so that a query can
// be compared against a slug directly.
func Slugify(s string) string {
	return strings.ReplaceAll(Normalize(s), " ", "-")
}

// FromSlug reconstructs a display title from a slug. It is only used for the
// handful of archive entries with no catalogue row: everything else carries the
// title Subscene itself showed.
func FromSlug(slug string) string {
	words := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

// YearFromSlug returns the year Subscene appended to a slug, or 0 when the slug
// carries none. A four-digit tail that is not a plausible release year (a serial
// number, an episode count) is not a year.
func YearFromSlug(slug string) int {
	m := yearSuffix.FindStringSubmatch(slug)
	if m == nil {
		return 0
	}
	year, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return year
}
